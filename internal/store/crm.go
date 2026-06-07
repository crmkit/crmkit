package store

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// ---- contacts ------------------------------------------------------------

// CreateContact inserts a contact. ID/timestamps are assigned if unset.
func (s *sqlStore) CreateContact(ws string, c *protocol.Contact) error {
	now := time.Now()
	if c.ID == "" {
		c.ID = protocol.NewID("c")
	}
	c.CreatedAt, c.UpdatedAt = now, now

	tags, err := marshalTags(c.Tags)
	if err != nil {
		return err
	}
	custom, err := marshalJSON(c.Custom)
	if err != nil {
		return err
	}
	_, err = s.exec(`
INSERT INTO contacts (id, workspace_id, name, email, phone, company_id, owner, stage, tags, notes, custom, follow_up_at, follow_up_note, created_at, updated_at, created_by)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, ws, c.Name, c.Email, c.Phone, c.CompanyID, c.Owner, c.Stage, tags, c.Notes, custom,
		nullableUnix(c.FollowUpAt), c.FollowUpNote, unix(now), unix(now), c.CreatedBy)
	if err != nil {
		return err
	}
	return s.fillContactRef(ws, c)
}

const contactColumns = `id, name, email, phone, company_id, owner, stage, tags, notes, custom, follow_up_at, follow_up_note, created_at, updated_at, created_by`

// GetContact loads one contact scoped to the workspace.
func (s *sqlStore) GetContact(ws, id string) (protocol.Contact, error) {
	row := s.queryRow(`SELECT `+contactColumns+` FROM contacts WHERE workspace_id = ? AND id = ?`, ws, id)
	c, err := scanContact(row)
	if err != nil {
		return protocol.Contact{}, err
	}
	if err := s.fillContactRef(ws, &c); err != nil {
		return protocol.Contact{}, err
	}
	return c, nil
}

// FindContactByEmail returns contacts in a workspace whose email matches
// (case-insensitive). Used by the upsert-on-create path.
func (s *sqlStore) FindContactByEmail(ws, email string) ([]protocol.Contact, error) {
	rows, err := s.query(`SELECT `+contactColumns+` FROM contacts
WHERE workspace_id = ? AND email IS NOT NULL AND lower(email) = lower(?)`, ws, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateContact persists the supplied contact (full overwrite of mutable
// fields) within a workspace and bumps updated_at.
func (s *sqlStore) UpdateContact(ws string, c *protocol.Contact) error {
	c.UpdatedAt = time.Now()
	tags, err := marshalTags(c.Tags)
	if err != nil {
		return err
	}
	custom, err := marshalJSON(c.Custom)
	if err != nil {
		return err
	}
	res, err := s.exec(`
UPDATE contacts SET name=?, email=?, phone=?, company_id=?, owner=?, stage=?, tags=?, notes=?, custom=?, follow_up_at=?, follow_up_note=?, updated_at=?
WHERE workspace_id = ? AND id = ?`,
		c.Name, c.Email, c.Phone, c.CompanyID, c.Owner, c.Stage, tags, c.Notes, custom,
		nullableUnix(c.FollowUpAt), c.FollowUpNote, unix(c.UpdatedAt), ws, c.ID)
	if err != nil {
		return err
	}
	if err := affectedOne(res); err != nil {
		return err
	}
	return s.fillContactRef(ws, c)
}

// DeleteContact removes a contact from a workspace.
func (s *sqlStore) DeleteContact(ws, id string) error {
	res, err := s.exec(`DELETE FROM contacts WHERE workspace_id = ? AND id = ?`, ws, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

func scanContact(sc scanner) (protocol.Contact, error) {
	var (
		c                  protocol.Contact
		email, phone       sql.NullString
		companyID, owner   sql.NullString
		stage, tags        sql.NullString
		notes, custom      sql.NullString
		followNote         sql.NullString
		createdBy          sql.NullString
		followAt           sql.NullInt64
		createdAt, updated int64
	)
	err := sc.Scan(&c.ID, &c.Name, &email, &phone, &companyID, &owner, &stage, &tags, &notes, &custom, &followAt, &followNote, &createdAt, &updated, &createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Contact{}, ErrNotFound
	}
	if err != nil {
		return protocol.Contact{}, err
	}
	c.Email, c.Phone = email.String, phone.String
	c.CompanyID, c.Owner, c.Stage = companyID.String, owner.String, stage.String
	c.Notes = notes.String
	c.Tags = unmarshalTags(tags.String)
	c.Custom = unmarshalCustom(custom.String)
	c.FollowUpAt = fromNullableUnix(followAt)
	c.FollowUpNote = followNote.String
	c.CreatedAt, c.UpdatedAt = fromUnix(createdAt), fromUnix(updated)
	c.CreatedBy = createdBy.String
	return c, nil
}

// ---- companies -----------------------------------------------------------

// CreateCompany inserts a company.
func (s *sqlStore) CreateCompany(ws string, c *protocol.Company) error {
	now := time.Now()
	if c.ID == "" {
		c.ID = protocol.NewID("co")
	}
	c.CreatedAt, c.UpdatedAt = now, now
	custom, err := marshalJSON(c.Custom)
	if err != nil {
		return err
	}
	tags, err := marshalTags(c.Tags)
	if err != nil {
		return err
	}
	_, err = s.exec(`
INSERT INTO companies (id, workspace_id, name, domain, tags, notes, custom, created_at, updated_at, created_by)
VALUES (?,?,?,?,?,?,?,?,?,?)`, c.ID, ws, c.Name, c.Domain, tags, c.Notes, custom, unix(now), unix(now), c.CreatedBy)
	return err
}

const companyColumns = `id, name, domain, tags, notes, custom, created_at, updated_at, created_by`

// GetCompany loads one company scoped to the workspace.
func (s *sqlStore) GetCompany(ws, id string) (protocol.Company, error) {
	row := s.queryRow(`SELECT `+companyColumns+` FROM companies WHERE workspace_id = ? AND id = ?`, ws, id)
	return scanCompany(row)
}

// FindCompanyByDomain returns companies in a workspace whose domain matches
// (case-insensitive). Used by the upsert-on-create path.
func (s *sqlStore) FindCompanyByDomain(ws, domain string) ([]protocol.Company, error) {
	rows, err := s.query(`SELECT `+companyColumns+` FROM companies
WHERE workspace_id = ? AND domain IS NOT NULL AND lower(domain) = lower(?)`, ws, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Company
	for rows.Next() {
		c, err := scanCompany(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCompany overwrites mutable company fields within a workspace.
func (s *sqlStore) UpdateCompany(ws string, c *protocol.Company) error {
	c.UpdatedAt = time.Now()
	custom, err := marshalJSON(c.Custom)
	if err != nil {
		return err
	}
	tags, err := marshalTags(c.Tags)
	if err != nil {
		return err
	}
	res, err := s.exec(`UPDATE companies SET name=?, domain=?, tags=?, notes=?, custom=?, updated_at=? WHERE workspace_id = ? AND id = ?`,
		c.Name, c.Domain, tags, c.Notes, custom, unix(c.UpdatedAt), ws, c.ID)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

// DeleteCompany removes a company from a workspace.
func (s *sqlStore) DeleteCompany(ws, id string) error {
	res, err := s.exec(`DELETE FROM companies WHERE workspace_id = ? AND id = ?`, ws, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

func scanCompany(sc scanner) (protocol.Company, error) {
	var (
		c              protocol.Company
		domain, custom sql.NullString
		tags, notes    sql.NullString
		createdBy      sql.NullString
		created, upd   int64
	)
	err := sc.Scan(&c.ID, &c.Name, &domain, &tags, &notes, &custom, &created, &upd, &createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Company{}, ErrNotFound
	}
	if err != nil {
		return protocol.Company{}, err
	}
	c.Domain = domain.String
	c.Tags = unmarshalTags(tags.String)
	c.Notes = notes.String
	c.Custom = unmarshalCustom(custom.String)
	c.CreatedAt, c.UpdatedAt = fromUnix(created), fromUnix(upd)
	c.CreatedBy = createdBy.String
	return c, nil
}

// ---- deals ---------------------------------------------------------------

// CreateDeal inserts a deal.
func (s *sqlStore) CreateDeal(ws string, d *protocol.Deal) error {
	now := time.Now()
	if d.ID == "" {
		d.ID = protocol.NewID("d")
	}
	if d.Status == "" {
		d.Status = "open"
	}
	d.CreatedAt, d.UpdatedAt = now, now
	custom, err := marshalJSON(d.Custom)
	if err != nil {
		return err
	}
	_, err = s.exec(`
INSERT INTO deals (id, workspace_id, title, contact_id, company_id, amount_cents, currency, stage, status, custom, follow_up_at, follow_up_note, created_at, updated_at, created_by)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, ws, d.Title, d.ContactID, d.CompanyID, d.AmountCents, d.Currency, d.Stage, d.Status, custom,
		nullableUnix(d.FollowUpAt), d.FollowUpNote, unix(now), unix(now), d.CreatedBy)
	if err != nil {
		return err
	}
	return s.fillDealRef(ws, d)
}

const dealColumns = `id, title, contact_id, company_id, amount_cents, currency, stage, status, custom, follow_up_at, follow_up_note, created_at, updated_at, created_by`

// GetDeal loads one deal scoped to the workspace.
func (s *sqlStore) GetDeal(ws, id string) (protocol.Deal, error) {
	row := s.queryRow(`SELECT `+dealColumns+` FROM deals WHERE workspace_id = ? AND id = ?`, ws, id)
	d, err := scanDeal(row)
	if err != nil {
		return protocol.Deal{}, err
	}
	if err := s.fillDealRef(ws, &d); err != nil {
		return protocol.Deal{}, err
	}
	return d, nil
}

// UpdateDeal overwrites mutable deal fields within a workspace.
func (s *sqlStore) UpdateDeal(ws string, d *protocol.Deal) error {
	d.UpdatedAt = time.Now()
	custom, err := marshalJSON(d.Custom)
	if err != nil {
		return err
	}
	res, err := s.exec(`
UPDATE deals SET title=?, contact_id=?, company_id=?, amount_cents=?, currency=?, stage=?, status=?, custom=?, follow_up_at=?, follow_up_note=?, updated_at=?
WHERE workspace_id = ? AND id = ?`,
		d.Title, d.ContactID, d.CompanyID, d.AmountCents, d.Currency, d.Stage, d.Status, custom,
		nullableUnix(d.FollowUpAt), d.FollowUpNote, unix(d.UpdatedAt), ws, d.ID)
	if err != nil {
		return err
	}
	if err := affectedOne(res); err != nil {
		return err
	}
	return s.fillDealRef(ws, d)
}

// DeleteDeal removes a deal from a workspace.
func (s *sqlStore) DeleteDeal(ws, id string) error {
	res, err := s.exec(`DELETE FROM deals WHERE workspace_id = ? AND id = ?`, ws, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

func scanDeal(sc scanner) (protocol.Deal, error) {
	var (
		d                       protocol.Deal
		contactID, companyID    sql.NullString
		currency, stage, status sql.NullString
		custom                  sql.NullString
		followNote              sql.NullString
		createdBy               sql.NullString
		amount                  sql.NullInt64
		followAt                sql.NullInt64
		created, upd            int64
	)
	err := sc.Scan(&d.ID, &d.Title, &contactID, &companyID, &amount, &currency, &stage, &status, &custom, &followAt, &followNote, &created, &upd, &createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Deal{}, ErrNotFound
	}
	if err != nil {
		return protocol.Deal{}, err
	}
	d.ContactID, d.CompanyID = contactID.String, companyID.String
	d.AmountCents = amount.Int64
	d.Currency, d.Stage, d.Status = currency.String, stage.String, status.String
	d.Custom = unmarshalCustom(custom.String)
	d.FollowUpAt = fromNullableUnix(followAt)
	d.FollowUpNote = followNote.String
	d.CreatedAt, d.UpdatedAt = fromUnix(created), fromUnix(upd)
	d.CreatedBy = createdBy.String
	return d, nil
}

// ---- reminders -----------------------------------------------------------

// ListReminders returns contacts and deals whose follow_up_at is at or before
// `until`, soonest first - the agent's "what needs attention now" view.
func (s *sqlStore) ListReminders(ws string, until time.Time, limit int) ([]protocol.Reminder, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	now := time.Now()
	out := []protocol.Reminder{}

	// Contacts first; fully read and close before the deals query because the
	// store may run on a single connection.
	crows, err := s.query(`SELECT id, name, email, follow_up_at, follow_up_note FROM contacts
WHERE workspace_id = ? AND follow_up_at IS NOT NULL AND follow_up_at <= ? ORDER BY follow_up_at ASC LIMIT ?`,
		ws, unix(until), limit)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var (
			id, name    string
			email, note sql.NullString
			fa          int64
		)
		if err := crows.Scan(&id, &name, &email, &fa, &note); err != nil {
			crows.Close()
			return nil, err
		}
		t := fromUnix(fa)
		out = append(out, protocol.Reminder{
			Handle: protocol.Handle(protocol.KindContact, id), Kind: protocol.KindContact,
			Title: name, Email: email.String, FollowUpAt: t, Note: note.String, Overdue: t.Before(now),
		})
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}

	drows, err := s.query(`SELECT id, title, follow_up_at, follow_up_note FROM deals
WHERE workspace_id = ? AND follow_up_at IS NOT NULL AND follow_up_at <= ? ORDER BY follow_up_at ASC LIMIT ?`,
		ws, unix(until), limit)
	if err != nil {
		return nil, err
	}
	for drows.Next() {
		var (
			id, title string
			note      sql.NullString
			fa        int64
		)
		if err := drows.Scan(&id, &title, &fa, &note); err != nil {
			drows.Close()
			return nil, err
		}
		t := fromUnix(fa)
		out = append(out, protocol.Reminder{
			Handle: protocol.Handle(protocol.KindDeal, id), Kind: protocol.KindDeal,
			Title: title, FollowUpAt: t, Note: note.String, Overdue: t.Before(now),
		})
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return nil, err
	}

	// Merge the two streams by due time, soonest first, and cap at limit.
	sort.Slice(out, func(i, j int) bool { return out[i].FollowUpAt.Before(out[j].FollowUpAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---- activities ----------------------------------------------------------

// CreateActivity logs an interaction.
func (s *sqlStore) CreateActivity(ws string, a *protocol.Activity) error {
	now := time.Now()
	if a.ID == "" {
		a.ID = protocol.NewID("act")
	}
	if a.Kind == "" {
		a.Kind = "note"
	}
	a.CreatedAt = now
	_, err := s.exec(`
INSERT INTO activities (id, workspace_id, contact_id, deal_id, company_id, kind, body, created_by, created_at)
VALUES (?,?,?,?,?,?,?,?,?)`, a.ID, ws, a.ContactID, a.DealID, a.CompanyID, a.Kind, a.Body, a.CreatedBy, unix(now))
	return err
}

// DeleteActivity removes one activity from a workspace (e.g. a mistaken entry, or
// to free room under the activity quota).
func (s *sqlStore) DeleteActivity(ws, id string) error {
	res, err := s.exec(`DELETE FROM activities WHERE workspace_id = ? AND id = ?`, ws, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

// ListActivities returns activities for a workspace, optionally filtered by
// contact, deal, or company, newest first.
func (s *sqlStore) ListActivities(ws, contactID, dealID, companyID string, limit int) ([]protocol.Activity, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	args := []any{ws}
	sb := strings.Builder{}
	sb.WriteString(`SELECT id, contact_id, deal_id, company_id, kind, body, created_by, created_at FROM activities WHERE workspace_id = ?`)
	if contactID != "" {
		sb.WriteString(` AND contact_id = ?`)
		args = append(args, contactID)
	}
	if dealID != "" {
		sb.WriteString(` AND deal_id = ?`)
		args = append(args, dealID)
	}
	if companyID != "" {
		sb.WriteString(` AND company_id = ?`)
		args = append(args, companyID)
	}
	sb.WriteString(` ORDER BY created_at DESC LIMIT ?`)
	args = append(args, limit)

	rows, err := s.query(sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []protocol.Activity{} // non-nil so an empty list serializes as [] not null
	for rows.Next() {
		var (
			a                                 protocol.Activity
			contact, deal, company, createdBy sql.NullString
			created                           int64
		)
		if err := rows.Scan(&a.ID, &contact, &deal, &company, &a.Kind, &a.Body, &createdBy, &created); err != nil {
			return nil, err
		}
		a.ContactID, a.DealID, a.CompanyID, a.CreatedBy = contact.String, deal.String, company.String, createdBy.String
		a.CreatedAt = fromUnix(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActivityStats returns the activity count and most-recent activity time for a
// contact or deal (whichever id is non-empty) - used to annotate a single-record
// fetch. Returns 0 + zero time when there are none.
func (s *sqlStore) ActivityStats(ws, contactID, dealID, companyID string) (int, time.Time, error) {
	args := []any{ws}
	sb := strings.Builder{}
	sb.WriteString(`SELECT count(*), coalesce(max(created_at), 0) FROM activities WHERE workspace_id = ?`)
	if contactID != "" {
		sb.WriteString(` AND contact_id = ?`)
		args = append(args, contactID)
	}
	if dealID != "" {
		sb.WriteString(` AND deal_id = ?`)
		args = append(args, dealID)
	}
	if companyID != "" {
		sb.WriteString(` AND company_id = ?`)
		args = append(args, companyID)
	}
	var (
		count int
		last  int64
	)
	if err := s.queryRow(sb.String(), args...).Scan(&count, &last); err != nil {
		return 0, time.Time{}, err
	}
	if last == 0 {
		return count, time.Time{}, nil
	}
	return count, fromUnix(last), nil
}

// ---- audit ---------------------------------------------------------------

// WriteAudit appends an audit entry attributed to the acting member
// (actorEmail). Failures are non-fatal to callers; they should log and continue.
func (s *sqlStore) WriteAudit(ws, tokenID, actorEmail, action, target, detail string) error {
	_, err := s.exec(`
INSERT INTO audit_log (id, workspace_id, token_id, actor_email, action, target, detail, created_at)
VALUES (?,?,?,?,?,?,?,?)`, protocol.NewID("aud"), ws, tokenID, actorEmail, action, target, detail, unix(time.Now()))
	return err
}

// PruneAuditForPlan deletes audit entries older than `before` (unix seconds)
// belonging to workspaces on the given plan, returning how many were removed.
// Retention is a per-plan window, so the sweep runs once per plan; workspaces on
// a plan not in the catalogue are simply left untouched (over-retain, the safe
// direction for a security log). The audit log is bounded by age, not count.
func (s *sqlStore) PruneAuditForPlan(plan string, before int64) (int64, error) {
	res, err := s.exec(
		`DELETE FROM audit_log WHERE created_at < ? AND workspace_id IN (SELECT id FROM workspaces WHERE plan = ?)`,
		before, plan)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AuditEntry is one recorded action.
type AuditEntry struct {
	ID         string
	TokenID    string
	ActorEmail string
	Action     string
	Target     string
	Detail     string
	CreatedAt  time.Time
}

// ListAudit returns recent audit entries for a workspace, newest first. When
// actorEmail is non-empty it returns only that member's actions (case-insensitive).
func (s *sqlStore) ListAudit(ws, actorEmail string, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	args := []any{ws}
	sb := strings.Builder{}
	sb.WriteString(`SELECT id, token_id, actor_email, action, target, detail, created_at FROM audit_log WHERE workspace_id = ?`)
	if actorEmail != "" {
		sb.WriteString(` AND lower(actor_email) = lower(?)`)
		args = append(args, actorEmail)
	}
	sb.WriteString(` ORDER BY created_at DESC LIMIT ?`)
	args = append(args, limit)

	rows, err := s.query(sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AuditEntry{}
	for rows.Next() {
		var (
			e              AuditEntry
			tokenID, actor sql.NullString
			target, detail sql.NullString
			created        int64
		)
		if err := rows.Scan(&e.ID, &tokenID, &actor, &e.Action, &target, &detail, &created); err != nil {
			return nil, err
		}
		e.TokenID, e.ActorEmail = tokenID.String, actor.String
		e.Target, e.Detail = target.String, detail.String
		e.CreatedAt = fromUnix(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- shared --------------------------------------------------------------

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func affectedOne(res interface{ RowsAffected() (int64, error) }) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- relation name resolution (read-time display enrichment) -------------

// namesByID returns id -> name for rows of `table` within the workspace, in a
// single query (the basis for resolving contact/company references without an
// N+1). `table` is code-controlled (only "contacts"/"companies" are passed), so
// the interpolation is injection-safe; the ids are bound parameters.
func (s *sqlStore) namesByID(ws, table string, ids []string) (map[string]string, error) {
	ids = distinctNonEmpty(ids)
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, ws)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.query("SELECT id, name FROM "+table+" WHERE workspace_id = ? AND id IN ("+ph+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func distinctNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// fillContactRefs resolves each contact's company_id to a company name (one
// query for the whole page), for display. Unresolved ids leave CompanyName empty
// (the render falls back to the id).
func (s *sqlStore) fillContactRefs(ws string, contacts []protocol.Contact) error {
	ids := make([]string, 0, len(contacts))
	for _, c := range contacts {
		ids = append(ids, c.CompanyID)
	}
	names, err := s.namesByID(ws, "companies", ids)
	if err != nil {
		return err
	}
	for i := range contacts {
		contacts[i].CompanyName = names[contacts[i].CompanyID]
	}
	return nil
}

func (s *sqlStore) fillContactRef(ws string, c *protocol.Contact) error {
	one := []protocol.Contact{*c}
	if err := s.fillContactRefs(ws, one); err != nil {
		return err
	}
	*c = one[0]
	return nil
}

// fillDealRefs resolves each deal's contact_id and company_id to names (two
// queries for the whole page), for display.
func (s *sqlStore) fillDealRefs(ws string, deals []protocol.Deal) error {
	contactIDs := make([]string, 0, len(deals))
	companyIDs := make([]string, 0, len(deals))
	for _, d := range deals {
		contactIDs = append(contactIDs, d.ContactID)
		companyIDs = append(companyIDs, d.CompanyID)
	}
	contactNames, err := s.namesByID(ws, "contacts", contactIDs)
	if err != nil {
		return err
	}
	companyNames, err := s.namesByID(ws, "companies", companyIDs)
	if err != nil {
		return err
	}
	for i := range deals {
		deals[i].ContactName = contactNames[deals[i].ContactID]
		deals[i].CompanyName = companyNames[deals[i].CompanyID]
	}
	return nil
}

func (s *sqlStore) fillDealRef(ws string, d *protocol.Deal) error {
	one := []protocol.Deal{*d}
	if err := s.fillDealRefs(ws, one); err != nil {
		return err
	}
	*d = one[0]
	return nil
}
