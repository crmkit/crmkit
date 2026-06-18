package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/crmkit/crmkit/internal/protocol"
)

// ErrBadCursor is returned when a pagination cursor cannot be decoded.
var ErrBadCursor = errors.New("bad cursor")

// QFilter is one already-validated filter condition. Column and Op are
// whitelisted by the caller (the HTTP layer maps user fields/operators to real
// columns and SQL operators); Value(s) are always bound parameters. Op "LIKE"
// is a sentinel the dialect maps to LIKE/ILIKE.
type QFilter struct {
	Column string
	Op     string // "=", "!=", ">", ">=", "<", "<=", "LIKE", "IN", "IS NULL", "IS NOT NULL"
	Value  any    // for binary ops
	Values []any  // for IN
	// JSONKey, when set, filters on a key inside the JSON `Column` (e.g. custom):
	// the comparison applies to the extracted text. Op is "=" or "LIKE". The key
	// is bound as a parameter, so it is injection-safe.
	JSONKey string
}

// Query is a validated list query: filters AND-ed together, an optional fuzzy
// search over SearchColumns, a single sort column with an id tiebreaker, a
// keyset Cursor, and a limit.
type Query struct {
	Filters       []QFilter
	Search        string
	SearchColumns []string
	SortColumn    string
	SortDesc      bool
	SortNumeric   bool // sort column is numeric (affects cursor value typing)
	Limit         int
	Cursor        *Cursor
}

// Cursor is an opaque keyset position: the sort value + id of the last row of
// the previous page, plus the sort it was produced under.
type Cursor struct {
	Col  string `json:"c"`
	Desc bool   `json:"d"`
	Num  bool   `json:"n"`
	Val  string `json:"v"`
	ID   string `json:"i"`
}

func encodeCursor(c Cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses an opaque cursor. Empty input yields (nil, nil).
func DecodeCursor(s string) (*Cursor, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrBadCursor
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil || c.Col == "" {
		return nil, ErrBadCursor
	}
	return &c, nil
}

func (q Query) effectiveSort() (col string, desc, numeric bool) {
	if q.Cursor != nil {
		return q.Cursor.Col, q.Cursor.Desc, q.Cursor.Num
	}
	return q.SortColumn, q.SortDesc, q.SortNumeric
}

// buildWhere assembles the WHERE clause shared by a query's page and its total
// count: workspace scope, the AND-ed filters, and the fuzzy search. Identifiers
// (filter columns, operators) are caller-whitelisted; every user value is a
// bound "?" parameter. It deliberately omits the keyset cursor (a pagination
// position, not a result filter), the sort, and the limit.
func (s *sqlStore) buildWhere(ws string, q Query) (string, []any) {
	args := []any{ws}
	var sb strings.Builder
	sb.WriteString(" WHERE workspace_id = ?")

	for _, f := range q.Filters {
		// A JSON-key filter compares a key inside the JSON column as text. The key
		// is bound (injection-safe); Op is "=" or "LIKE".
		if f.JSONKey != "" {
			expr, keyArg := s.d.jsonText(f.Column, f.JSONKey)
			op := f.Op
			if op == "LIKE" {
				op = s.d.like
			}
			fmt.Fprintf(&sb, " AND %s %s ?", expr, op)
			args = append(args, keyArg, f.Value)
			continue
		}
		switch f.Op {
		case "IS NULL", "IS NOT NULL":
			fmt.Fprintf(&sb, " AND %s %s", f.Column, f.Op)
		case "IN":
			if len(f.Values) == 0 {
				continue
			}
			ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Values)), ",")
			fmt.Fprintf(&sb, " AND %s IN (%s)", f.Column, ph)
			args = append(args, f.Values...)
		case "LIKE":
			fmt.Fprintf(&sb, " AND %s %s ?", f.Column, s.d.like)
			args = append(args, f.Value)
		default:
			fmt.Fprintf(&sb, " AND %s %s ?", f.Column, f.Op)
			args = append(args, f.Value)
		}
	}

	if q.Search != "" && len(q.SearchColumns) > 0 {
		like := "%" + q.Search + "%"
		parts := make([]string, len(q.SearchColumns))
		for i, col := range q.SearchColumns {
			parts[i] = fmt.Sprintf("%s %s ?", col, s.d.like)
			args = append(args, like)
		}
		fmt.Fprintf(&sb, " AND (%s)", strings.Join(parts, " OR "))
	}

	return sb.String(), args
}

// buildListSQL assembles the SELECT for a query's page. It fetches Limit+1 rows
// so the caller can tell whether a next page exists.
func (s *sqlStore) buildListSQL(table, columns, ws string, q Query) (string, []any) {
	where, args := s.buildWhere(ws, q)
	var sb strings.Builder
	sb.WriteString("SELECT " + columns + " FROM " + table)
	sb.WriteString(where)

	col, desc, numeric := q.effectiveSort()
	cmp := ">"
	dir := "ASC"
	if desc {
		cmp, dir = "<", "DESC"
	}
	if q.Cursor != nil {
		var v any = q.Cursor.Val
		if numeric {
			n, _ := strconv.ParseInt(q.Cursor.Val, 10, 64)
			v = n
		}
		// Row-value comparison continues the keyset; supported by both backends.
		fmt.Fprintf(&sb, " AND (%s, id) %s (?, ?)", col, cmp)
		args = append(args, v, q.Cursor.ID)
	}
	fmt.Fprintf(&sb, " ORDER BY %s %s, id %s LIMIT ?", col, dir, dir)
	args = append(args, q.Limit+1)
	return sb.String(), args
}

func (q Query) nextCursor(valStr, id string) string {
	col, desc, num := q.effectiveSort()
	return encodeCursor(Cursor{Col: col, Desc: desc, Num: num, Val: valStr, ID: id})
}

// countMatching returns the total number of rows matching the query's filters
// and search (workspace-scoped), independent of the page or cursor. It's a
// COUNT(*) over the same WHERE clause as the list, giving callers a "X of N"
// hint alongside keyset pagination.
func (s *sqlStore) countMatching(table, ws string, q Query) (int, error) {
	where, args := s.buildWhere(ws, q)
	return s.countWhere("SELECT COUNT(*) FROM "+table+where, args...)
}

// QueryContacts runs a validated query and returns the page, the total number
// of rows matching the filters, and a next-cursor (empty when there are no more
// rows).
func (s *sqlStore) QueryContacts(ws string, q Query) ([]protocol.Contact, int, string, error) {
	sqlStr, args := s.buildListSQL("contacts", contactColumns, ws, q)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, 0, "", err
	}
	out := []protocol.Contact{} // non-nil so an empty list serializes as [] not null
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			rows.Close()
			return nil, 0, "", err
		}
		out = append(out, c)
	}
	err = rows.Err()
	rows.Close() // close before the relation-resolution query (single-connection safety)
	if err != nil {
		return nil, 0, "", err
	}
	next := ""
	if len(out) > q.Limit {
		last := out[q.Limit-1]
		out = out[:q.Limit]
		col, _, _ := q.effectiveSort()
		next = q.nextCursor(contactSortVal(last, col), last.ID)
	}
	total, err := s.countMatching("contacts", ws, q)
	if err != nil {
		return nil, 0, "", err
	}
	if err := s.fillContactRefs(ws, out); err != nil {
		return nil, 0, "", err
	}
	return out, total, next, nil
}

// QueryCompanies runs a validated query over companies.
func (s *sqlStore) QueryCompanies(ws string, q Query) ([]protocol.Company, int, string, error) {
	sqlStr, args := s.buildListSQL("companies", companyColumns, ws, q)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, 0, "", err
	}
	out := []protocol.Company{}
	for rows.Next() {
		c, err := scanCompany(rows)
		if err != nil {
			rows.Close()
			return nil, 0, "", err
		}
		out = append(out, c)
	}
	err = rows.Err()
	rows.Close() // close before the count query (single-connection safety)
	if err != nil {
		return nil, 0, "", err
	}
	next := ""
	if len(out) > q.Limit {
		last := out[q.Limit-1]
		out = out[:q.Limit]
		col, _, _ := q.effectiveSort()
		next = q.nextCursor(companySortVal(last, col), last.ID)
	}
	total, err := s.countMatching("companies", ws, q)
	if err != nil {
		return nil, 0, "", err
	}
	return out, total, next, nil
}

// QueryDeals runs a validated query over deals.
func (s *sqlStore) QueryDeals(ws string, q Query) ([]protocol.Deal, int, string, error) {
	sqlStr, args := s.buildListSQL("deals", dealColumns, ws, q)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, 0, "", err
	}
	out := []protocol.Deal{}
	for rows.Next() {
		d, err := scanDeal(rows)
		if err != nil {
			rows.Close()
			return nil, 0, "", err
		}
		out = append(out, d)
	}
	err = rows.Err()
	rows.Close() // close before the relation-resolution query (single-connection safety)
	if err != nil {
		return nil, 0, "", err
	}
	next := ""
	if len(out) > q.Limit {
		last := out[q.Limit-1]
		out = out[:q.Limit]
		col, _, _ := q.effectiveSort()
		next = q.nextCursor(dealSortVal(last, col), last.ID)
	}
	total, err := s.countMatching("deals", ws, q)
	if err != nil {
		return nil, 0, "", err
	}
	if err := s.fillDealRefs(ws, out); err != nil {
		return nil, 0, "", err
	}
	return out, total, next, nil
}

// QueryTickets runs a validated query over tickets.
func (s *sqlStore) QueryTickets(ws string, q Query) ([]protocol.Ticket, int, string, error) {
	sqlStr, args := s.buildListSQL("tickets", ticketColumns, ws, q)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, 0, "", err
	}
	out := []protocol.Ticket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			rows.Close()
			return nil, 0, "", err
		}
		out = append(out, t)
	}
	err = rows.Err()
	rows.Close() // close before the relation-resolution query (single-connection safety)
	if err != nil {
		return nil, 0, "", err
	}
	next := ""
	if len(out) > q.Limit {
		last := out[q.Limit-1]
		out = out[:q.Limit]
		col, _, _ := q.effectiveSort()
		next = q.nextCursor(ticketSortVal(last, col), last.ID)
	}
	total, err := s.countMatching("tickets", ws, q)
	if err != nil {
		return nil, 0, "", err
	}
	if err := s.fillTicketRefs(ws, out); err != nil {
		return nil, 0, "", err
	}
	return out, total, next, nil
}

// QueryTasks runs a validated query over tasks.
func (s *sqlStore) QueryTasks(ws string, q Query) ([]protocol.Task, int, string, error) {
	sqlStr, args := s.buildListSQL("tasks", taskColumns, ws, q)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, 0, "", err
	}
	out := []protocol.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, 0, "", err
		}
		out = append(out, t)
	}
	err = rows.Err()
	rows.Close() // close before the relation-resolution query (single-connection safety)
	if err != nil {
		return nil, 0, "", err
	}
	next := ""
	if len(out) > q.Limit {
		last := out[q.Limit-1]
		out = out[:q.Limit]
		col, _, _ := q.effectiveSort()
		next = q.nextCursor(taskSortVal(last, col), last.ID)
	}
	total, err := s.countMatching("tasks", ws, q)
	if err != nil {
		return nil, 0, "", err
	}
	if err := s.fillTaskRefs(ws, out); err != nil {
		return nil, 0, "", err
	}
	return out, total, next, nil
}

// QueryCampaigns runs a validated query over campaigns.
func (s *sqlStore) QueryCampaigns(ws string, q Query) ([]protocol.Campaign, int, string, error) {
	sqlStr, args := s.buildListSQL("campaigns", campaignColumns, ws, q)
	rows, err := s.query(sqlStr, args...)
	if err != nil {
		return nil, 0, "", err
	}
	out := []protocol.Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			rows.Close()
			return nil, 0, "", err
		}
		out = append(out, c)
	}
	err = rows.Err()
	rows.Close() // close before the count query (single-connection safety)
	if err != nil {
		return nil, 0, "", err
	}
	next := ""
	if len(out) > q.Limit {
		last := out[q.Limit-1]
		out = out[:q.Limit]
		col, _, _ := q.effectiveSort()
		next = q.nextCursor(campaignSortVal(last, col), last.ID)
	}
	total, err := s.countMatching("campaigns", ws, q)
	if err != nil {
		return nil, 0, "", err
	}
	return out, total, next, nil
}

func campaignSortVal(c protocol.Campaign, col string) string {
	switch col {
	case "name":
		return c.Name
	case "created_at":
		return strconv.FormatInt(c.CreatedAt.Unix(), 10)
	default:
		return strconv.FormatInt(c.UpdatedAt.Unix(), 10)
	}
}

func ticketSortVal(t protocol.Ticket, col string) string {
	switch col {
	case "subject":
		return t.Subject
	case "created_at":
		return strconv.FormatInt(t.CreatedAt.Unix(), 10)
	default:
		return strconv.FormatInt(t.UpdatedAt.Unix(), 10)
	}
}

func taskSortVal(t protocol.Task, col string) string {
	switch col {
	case "title":
		return t.Title
	case "due_at":
		if t.DueAt == nil {
			return "0"
		}
		return strconv.FormatInt(t.DueAt.Unix(), 10)
	case "created_at":
		return strconv.FormatInt(t.CreatedAt.Unix(), 10)
	default:
		return strconv.FormatInt(t.UpdatedAt.Unix(), 10)
	}
}

func contactSortVal(c protocol.Contact, col string) string {
	switch col {
	case "name":
		return c.Name
	case "created_at":
		return strconv.FormatInt(c.CreatedAt.Unix(), 10)
	default:
		return strconv.FormatInt(c.UpdatedAt.Unix(), 10)
	}
}

func companySortVal(c protocol.Company, col string) string {
	switch col {
	case "name":
		return c.Name
	case "created_at":
		return strconv.FormatInt(c.CreatedAt.Unix(), 10)
	default:
		return strconv.FormatInt(c.UpdatedAt.Unix(), 10)
	}
}

func dealSortVal(d protocol.Deal, col string) string {
	switch col {
	case "title":
		return d.Title
	case "amount_cents":
		return strconv.FormatInt(d.AmountCents, 10)
	case "created_at":
		return strconv.FormatInt(d.CreatedAt.Unix(), 10)
	default:
		return strconv.FormatInt(d.UpdatedAt.Unix(), 10)
	}
}
