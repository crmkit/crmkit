package render

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// stampLayout is compact RFC3339: date + time to the minute + offset, e.g.
// 2026-06-06T07:14-07:00 (or ...Z for UTC). Unambiguous and standard enough for
// any LLM to parse, without the noise of seconds. The time is formatted in its
// own location, so callers localize to the workspace tz before rendering.
const stampLayout = "2006-01-02T15:04Z07:00"

// Stamp formats an instant as compact RFC3339 in its own location ("" if zero).
// Exported so callers rendering times outside the entity helpers (e.g. the audit
// log) share one format.
func Stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(stampLayout)
}

func date(t time.Time) string {
	return Stamp(t)
}

func datep(t *time.Time) string {
	if t == nil {
		return ""
	}
	return date(*t)
}

func money(cents int64, currency string) string {
	if cents == 0 {
		return ""
	}
	cur := currency
	if cur == "" {
		cur = "USD"
	}
	return fmt.Sprintf("%.2f %s", float64(cents)/100, cur)
}

func customFields(custom map[string]any) []Field {
	if len(custom) == 0 {
		return nil
	}
	keys := make([]string, 0, len(custom))
	for k := range custom {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Field, 0, len(keys))
	for _, k := range keys {
		out = append(out, F("custom."+k, fmt.Sprint(custom[k])))
	}
	return out
}

// ---- contacts ------------------------------------------------------------

// ContactLine renders a contact as one grepable line.
func ContactLine(c protocol.Contact) string {
	return Line(protocol.FormatRef(protocol.KindContact, c.Handle),
		F("name", c.Name),
		F("email", c.Email),
		F("phone", c.Phone),
		F("company", nameOrID(c.CompanyName, c.CompanyHandle)),
		F("stage", c.Stage),
		F("owner", c.Owner),
		F("tags", strings.Join(c.Tags, ",")),
		F("updated", date(c.UpdatedAt)),
	)
}

// Contacts renders a list of contacts with a trailing count summary.
func Contacts(list []protocol.Contact) string {
	b := strings.Builder{}
	for _, c := range list {
		b.WriteString(ContactLine(c))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %d contact(s)", len(list))
	return b.String()
}

// Contact renders a full contact detail block.
func Contact(c protocol.Contact) string {
	fields := []Field{
		F("handle", protocol.FormatRef(protocol.KindContact, c.Handle)),
		F("version", verStr(c.Version)),
		F("name", c.Name),
		F("email", c.Email),
		F("phone", c.Phone),
		F("company", c.CompanyName),
		F("company_ref", c.CompanyHandle),
		F("stage", c.Stage),
		F("owner", c.Owner),
		F("tags", strings.Join(c.Tags, ", ")),
		F("notes", c.Notes),
		F("created", date(c.CreatedAt)),
		F("created_by", c.CreatedBy),
		F("updated", date(c.UpdatedAt)),
		F("activities", countField(c.ActivityCount)),
		F("last_activity", datep(c.LastActivityAt)),
	}
	fields = append(fields, customFields(c.Custom)...)
	return Record(fields...)
}

// ---- search --------------------------------------------------------------

// SearchResults renders grouped cross-entity search hits: a section per
// non-empty type (reusing the per-entity line format) and a combined summary
// naming the query, so an agent can grep by handle or by type header.
func SearchResults(query string, contacts []protocol.Contact, companies []protocol.Company, deals []protocol.Deal, tickets []protocol.Ticket) string {
	b := strings.Builder{}
	if len(contacts) > 0 {
		b.WriteString("# contacts\n")
		for _, c := range contacts {
			b.WriteString(ContactLine(c))
			b.WriteByte('\n')
		}
	}
	if len(companies) > 0 {
		b.WriteString("# companies\n")
		for _, c := range companies {
			b.WriteString(CompanyLine(c))
			b.WriteByte('\n')
		}
	}
	if len(deals) > 0 {
		b.WriteString("# deals\n")
		for _, d := range deals {
			b.WriteString(DealLine(d))
			b.WriteByte('\n')
		}
	}
	if len(tickets) > 0 {
		b.WriteString("# tickets\n")
		for _, t := range tickets {
			b.WriteString(TicketLine(t))
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "# %d contact(s), %d company(ies), %d deal(s), %d ticket(s) for %q",
		len(contacts), len(companies), len(deals), len(tickets), query)
	return b.String()
}

// ---- companies -----------------------------------------------------------

// CompanyLine renders a company as one grepable line.
func CompanyLine(c protocol.Company) string {
	return Line(protocol.FormatRef(protocol.KindCompany, c.Handle),
		F("name", c.Name),
		F("domain", c.Domain),
		F("tags", strings.Join(c.Tags, ",")),
		F("updated", date(c.UpdatedAt)),
	)
}

// Companies renders a list of companies with a trailing count summary.
func Companies(list []protocol.Company) string {
	b := strings.Builder{}
	for _, c := range list {
		b.WriteString(CompanyLine(c))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %d company(ies)", len(list))
	return b.String()
}

// Company renders a full company detail block.
func Company(c protocol.Company) string {
	fields := []Field{
		F("handle", protocol.FormatRef(protocol.KindCompany, c.Handle)),
		F("version", verStr(c.Version)),
		F("name", c.Name),
		F("domain", c.Domain),
		F("tags", strings.Join(c.Tags, ", ")),
		F("notes", c.Notes),
		F("created", date(c.CreatedAt)),
		F("created_by", c.CreatedBy),
		F("updated", date(c.UpdatedAt)),
		F("activities", countField(c.ActivityCount)),
		F("last_activity", datep(c.LastActivityAt)),
	}
	fields = append(fields, customFields(c.Custom)...)
	return Record(fields...)
}

// ---- deals ---------------------------------------------------------------

// DealLine renders a deal as one grepable line.
func DealLine(d protocol.Deal) string {
	return Line(protocol.FormatRef(protocol.KindDeal, d.Handle),
		F("title", d.Title),
		F("amount", money(d.AmountCents, d.Currency)),
		F("stage", d.Stage),
		F("status", d.Status),
		F("contact", nameOrID(d.ContactName, d.ContactHandle)),
		F("company", nameOrID(d.CompanyName, d.CompanyHandle)),
		F("updated", date(d.UpdatedAt)),
	)
}

// Deals renders a list of deals with a trailing count + pipeline-value summary.
func Deals(list []protocol.Deal) string {
	b := strings.Builder{}
	var total int64
	for _, d := range list {
		b.WriteString(DealLine(d))
		b.WriteByte('\n')
		if d.Status == "" || d.Status == "open" {
			total += d.AmountCents
		}
	}
	fmt.Fprintf(&b, "# %d deal(s), open pipeline %s", len(list), fallback(money(total, "USD"), "0.00 USD"))
	return b.String()
}

// Deal renders a full deal detail block.
func Deal(d protocol.Deal) string {
	fields := []Field{
		F("handle", protocol.FormatRef(protocol.KindDeal, d.Handle)),
		F("version", verStr(d.Version)),
		F("title", d.Title),
		F("amount", money(d.AmountCents, d.Currency)),
		F("stage", d.Stage),
		F("status", d.Status),
		F("contact", d.ContactName),
		F("contact_ref", d.ContactHandle),
		F("company", d.CompanyName),
		F("company_ref", d.CompanyHandle),
		F("created", date(d.CreatedAt)),
		F("created_by", d.CreatedBy),
		F("updated", date(d.UpdatedAt)),
		F("activities", countField(d.ActivityCount)),
		F("last_activity", datep(d.LastActivityAt)),
	}
	fields = append(fields, customFields(d.Custom)...)
	return Record(fields...)
}

// ---- tickets -------------------------------------------------------------

// TicketLine renders a ticket as one grepable line.
func TicketLine(t protocol.Ticket) string {
	return Line(protocol.FormatRef(protocol.KindTicket, t.Handle),
		F("subject", t.Subject),
		F("status", t.Status),
		F("requester", nameOrID(t.RequesterName, t.RequesterHandle)),
		F("assignee", t.Assignee),
		F("tags", strings.Join(t.Tags, ",")),
		F("updated", date(t.UpdatedAt)),
	)
}

// Tickets renders a list of tickets with a trailing count summary.
func Tickets(list []protocol.Ticket) string {
	b := strings.Builder{}
	for _, t := range list {
		b.WriteString(TicketLine(t))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %d ticket(s)", len(list))
	return b.String()
}

// Ticket renders a full ticket detail block.
func Ticket(t protocol.Ticket) string {
	fields := []Field{
		F("handle", protocol.FormatRef(protocol.KindTicket, t.Handle)),
		F("version", verStr(t.Version)),
		F("subject", t.Subject),
		F("status", t.Status),
		F("requester", t.RequesterName),
		F("requester_ref", t.RequesterHandle),
		F("assignee", t.Assignee),
		F("tags", strings.Join(t.Tags, ", ")),
		F("content", t.Content),
		F("created", date(t.CreatedAt)),
		F("created_by", t.CreatedBy),
		F("updated", date(t.UpdatedAt)),
		F("activities", countField(t.ActivityCount)),
		F("last_activity", datep(t.LastActivityAt)),
	}
	fields = append(fields, customFields(t.Custom)...)
	return Record(fields...)
}

// ---- campaigns -----------------------------------------------------------

// CampaignLine renders a campaign as one grepable line.
func CampaignLine(c protocol.Campaign) string {
	return Line(protocol.FormatRef(protocol.KindCampaign, c.Handle),
		F("name", c.Name),
		F("status", c.Status),
		F("updated", date(c.UpdatedAt)),
	)
}

// Campaigns renders a list of campaigns with a trailing count summary.
func Campaigns(list []protocol.Campaign) string {
	b := strings.Builder{}
	for _, c := range list {
		b.WriteString(CampaignLine(c))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %d campaign(s)", len(list))
	return b.String()
}

// Campaign renders a full campaign detail block, including member counts.
func Campaign(c protocol.Campaign) string {
	fields := []Field{
		F("handle", protocol.FormatRef(protocol.KindCampaign, c.Handle)),
		F("version", verStr(c.Version)),
		F("name", c.Name),
		F("status", c.Status),
		F("description", c.Description),
		F("contacts", countField(c.MemberCounts["contact"])),
		F("companies", countField(c.MemberCounts["company"])),
		F("created", date(c.CreatedAt)),
		F("created_by", c.CreatedBy),
		F("updated", date(c.UpdatedAt)),
	}
	fields = append(fields, customFields(c.Custom)...)
	return Record(fields...)
}

// CampaignMemberLine renders one campaign member as a grepable line.
func CampaignMemberLine(m protocol.CampaignMember) string {
	return Line(protocol.FormatRef(m.Kind, m.Handle),
		F("kind", m.Kind),
		F("name", m.Name),
		F("reason", m.Reason),
		F("added", date(m.AddedAt)),
	)
}

// CampaignMembers renders a campaign's members with a trailing count summary.
func CampaignMembers(list []protocol.CampaignMember) string {
	b := strings.Builder{}
	for _, m := range list {
		b.WriteString(CampaignMemberLine(m))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %d member(s)", len(list))
	return b.String()
}

// ---- activities ----------------------------------------------------------

// ActivityLine renders an activity as one grepable line.
func ActivityLine(a protocol.Activity) string {
	return Line(protocol.FormatRef(protocol.KindActivity, a.Handle),
		F("kind", a.Kind),
		F("by", a.CreatedBy),
		F("contact", a.ContactHandle),
		F("deal", a.DealHandle),
		F("company", a.CompanyHandle),
		F("ticket", a.TicketHandle),
		F("at", date(a.CreatedAt)),
		F("body", a.Body),
	)
}

// Activities renders a list of activities with a trailing count summary.
func Activities(list []protocol.Activity) string {
	b := strings.Builder{}
	for _, a := range list {
		b.WriteString(ActivityLine(a))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %d activity(ies)", len(list))
	return b.String()
}

// ---- tasks ---------------------------------------------------------------

// taskAbout returns the first non-empty linked-record ref on a task, for the
// at-a-glance "about" column. Full detail shows every link separately.
func taskAbout(t protocol.Task) string {
	return fallback(t.ContactRef, fallback(t.CompanyRef, fallback(t.DealRef, t.TicketRef)))
}

// doneFlag renders a task's completion as a short yes/"".
func doneFlag(t protocol.Task) string {
	if t.DoneAt != nil {
		return "yes"
	}
	return ""
}

// TaskLine renders a task as one grepable line.
func TaskLine(t protocol.Task) string {
	return Line(protocol.FormatRef(protocol.KindTask, t.Handle),
		F("title", t.Title),
		F("due", datep(t.DueAt)),
		F("done", doneFlag(t)),
		F("assignee", t.Assignee),
		F("about", taskAbout(t)),
	)
}

// Tasks renders a list of tasks with a count + open summary.
func Tasks(list []protocol.Task) string {
	b := strings.Builder{}
	open := 0
	for _, t := range list {
		b.WriteString(TaskLine(t))
		b.WriteByte('\n')
		if t.DoneAt == nil {
			open++
		}
	}
	fmt.Fprintf(&b, "# %d task(s), %d open", len(list), open)
	return b.String()
}

// Task renders a full task detail block.
func Task(t protocol.Task) string {
	fields := []Field{
		F("handle", protocol.FormatRef(protocol.KindTask, t.Handle)),
		F("version", verStr(t.Version)),
		F("title", t.Title),
		F("due", datep(t.DueAt)),
		F("done", doneFlag(t)),
		F("done_at", datep(t.DoneAt)),
		F("assignee", t.Assignee),
		F("contact_ref", t.ContactRef),
		F("company_ref", t.CompanyRef),
		F("deal_ref", t.DealRef),
		F("ticket_ref", t.TicketRef),
		F("created", date(t.CreatedAt)),
		F("created_by", t.CreatedBy),
		F("updated", date(t.UpdatedAt)),
	}
	fields = append(fields, customFields(t.Custom)...)
	return Record(fields...)
}

// ---- reminders -----------------------------------------------------------

// ReminderLine renders a due/overdue task as one grepable line.
func ReminderLine(r protocol.Reminder) string {
	overdue := ""
	if r.Overdue {
		overdue = "yes"
	}
	return Line(r.Handle,
		F("due", date(r.FollowUpAt)),
		F("overdue", overdue),
		F("title", r.Title),
		F("about", r.About),
		F("assignee", r.Assignee),
	)
}

// Reminders renders a list of reminders with a count + overdue summary.
func Reminders(list []protocol.Reminder) string {
	b := strings.Builder{}
	overdue := 0
	for _, r := range list {
		b.WriteString(ReminderLine(r))
		b.WriteByte('\n')
		if r.Overdue {
			overdue++
		}
	}
	fmt.Fprintf(&b, "# %d reminder(s), %d overdue", len(list), overdue)
	return b.String()
}

// ---- workspaces / members / invites --------------------------------------

// WorkspaceLine renders a workspace membership as one grepable line.
func WorkspaceLine(w protocol.Workspace) string {
	return Line("workspace/"+w.ID,
		F("name", w.Name),
		F("role", w.Role),
		F("created", date(w.CreatedAt)),
	)
}

// Workspaces renders a list of workspaces with a trailing count summary.
func Workspaces(list []protocol.Workspace) string {
	b := strings.Builder{}
	for _, w := range list {
		b.WriteString(WorkspaceLine(w))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %d workspace(s)", len(list))
	return b.String()
}

// WorkspacesHint renders a short pointer, for the login response, listing the
// other workspaces a user can switch into by minting a scoped token.
func WorkspacesHint(list []protocol.Workspace) string {
	if len(list) <= 1 {
		return ""
	}
	b := strings.Builder{}
	b.WriteString("\nYou belong to other workspaces. To act in one, mint a token:\n")
	for _, w := range list {
		fmt.Fprintf(&b, "  POST /workspaces/%s/tokens   (%s, role=%s)\n", w.ID, w.Name, w.Role)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Members renders workspace members and any pending invites.
func Members(members []protocol.Member, invites []protocol.Invite) string {
	b := strings.Builder{}
	for _, m := range members {
		b.WriteString(Line("member/"+m.UserID,
			F("email", m.Email),
			F("role", m.Role),
			F("joined", date(m.CreatedAt)),
		))
		b.WriteByte('\n')
	}
	for _, inv := range invites {
		b.WriteString(Line("invite/"+inv.ID,
			F("email", inv.Email),
			F("role", inv.Role),
			F("status", "pending"),
		))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "# %d member(s), %d pending invite(s)", len(members), len(invites))
	return b.String()
}

func fallback(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// nameOrID returns the human-readable name when a relation has been resolved,
// otherwise the raw id (e.g. a deleted/unresolved reference). Empty when both
// are empty, so the field is omitted.
func nameOrID(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

// countField renders a non-negative count, or "" for zero so the field is
// omitted (no point showing activities=0).
func countField(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// verStr renders a record version (the optimistic-concurrency token), or "" for
// an unset value so the field is omitted.
func verStr(v int64) string {
	if v <= 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

// Int parses an integer query value, returning def on failure.
func Int(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
