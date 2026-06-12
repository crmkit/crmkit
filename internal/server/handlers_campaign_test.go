package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestCampaignCRUD(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// Create a campaign: name required, description is the free-text brief,
	// custom carries free-form extension fields like every other entity.
	s, b := do(t, ts, "POST", "/campaigns", token,
		`{"name":"Series-B fintechs","description":"CTOs at fintechs that raised a Series B","custom":{"region":"EMEA"}}`)
	if s != http.StatusCreated {
		t.Fatalf("create campaign: %d %q", s, b)
	}
	for _, want := range []string{"Series-B fintechs", "status:", "active", "CTOs at fintechs", "region", "EMEA"} {
		if !strings.Contains(b, want) {
			t.Fatalf("create response missing %q:\n%s", want, b)
		}
	}
	cid := firstHandleID(b, "campaign/")
	if cid == "" {
		t.Fatalf("no campaign handle: %s", b)
	}

	// Name is required.
	if s, _ := do(t, ts, "POST", "/campaigns", token, `{"description":"no name"}`); s != http.StatusBadRequest {
		t.Fatalf("missing name should 400, got %d", s)
	}

	// List + count.
	if s, lb := do(t, ts, "GET", "/campaigns", token, ""); s != 200 || !strings.Contains(lb, "Series-B fintechs") || !strings.Contains(lb, "# 1 campaign") {
		t.Fatalf("list campaigns: %d %q", s, lb)
	}

	// Status filter.
	if _, fb := do(t, ts, "GET", "/campaigns?status=active", token, ""); !strings.Contains(fb, "Series-B fintechs") {
		t.Fatalf("status=active should match:\n%s", fb)
	}
	if _, fb := do(t, ts, "GET", "/campaigns?status=done", token, ""); strings.Contains(fb, "Series-B fintechs") {
		t.Fatalf("status=done must not match an active campaign:\n%s", fb)
	}

	// Update status; invalid status rejected.
	if s, ub := do(t, ts, "PATCH", "/campaigns/"+cid, token, `{"status":"done"}`); s != 200 || !strings.Contains(ub, "done") {
		t.Fatalf("update status: %d %q", s, ub)
	}
	if s, _ := do(t, ts, "PATCH", "/campaigns/"+cid, token, `{"status":"banana"}`); s != http.StatusBadRequest {
		t.Fatalf("invalid status should 400, got %d", s)
	}

	// Delete is two-step.
	s, gb := do(t, ts, "DELETE", "/campaigns/"+cid, token, "")
	if s != http.StatusConflict || !strings.Contains(gb, "confirm=") {
		t.Fatalf("delete should gate with a confirm token: %d %q", s, gb)
	}
	i := strings.LastIndex(gb, "confirm=") + len("confirm=")
	confirm := gb[i : i+8]
	if s, _ := do(t, ts, "DELETE", "/campaigns/"+cid+"?confirm="+confirm, token, ""); s != 200 {
		t.Fatalf("confirmed delete should 200, got %d", s)
	}
	if s, _ := do(t, ts, "GET", "/campaigns/"+cid, token, ""); s != http.StatusNotFound {
		t.Fatalf("deleted campaign should 404, got %d", s)
	}
}

func TestCampaignMembership(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// A campaign and a contact + company to gather under it.
	_, cb := do(t, ts, "POST", "/campaigns", token, `{"name":"Q3 outbound"}`)
	cid := firstHandleID(cb, "campaign/")
	_, ctb := do(t, ts, "POST", "/contacts", token, `{"name":"Jane","email":"jane@acme.com"}`)
	contactID := firstHandleID(ctb, "contact/")
	_, cob := do(t, ts, "POST", "/companies", token, `{"name":"Acme","domain":"acme.com"}`)
	companyID := firstHandleID(cob, "company/")
	if cid == "" || contactID == "" || companyID == "" {
		t.Fatalf("setup handles: campaign=%q contact=%q company=%q", cid, contactID, companyID)
	}

	// Attach the contact with a provenance reason.
	if s, ab := do(t, ts, "POST", "/campaigns/"+cid+"/members", token,
		`{"kind":"contact","id":"contact_`+contactID+`","reason":"matches the brief"}`); s != 200 || !strings.Contains(ab, "OK attached") {
		t.Fatalf("attach contact: %d %q", s, ab)
	}

	// Re-attaching the SAME contact is an idempotent no-op (the anti-waste guarantee):
	// it still succeeds and the member count stays at one.
	if s, _ := do(t, ts, "POST", "/campaigns/"+cid+"/members", token,
		`{"kind":"contact","id":"contact_`+contactID+`"}`); s != 200 {
		t.Fatalf("re-attach should be an idempotent 200, got %d", s)
	}

	// Attach the company too.
	if s, _ := do(t, ts, "POST", "/campaigns/"+cid+"/members", token,
		`{"kind":"company","id":"company_`+companyID+`"}`); s != 200 {
		t.Fatalf("attach company: %d", s)
	}

	// Member list shows both, exactly once each (dedup), with the contact's reason.
	_, mb := do(t, ts, "GET", "/campaigns/"+cid+"/members", token, "")
	if !strings.Contains(mb, "Jane") || !strings.Contains(mb, "Acme") || !strings.Contains(mb, "matches the brief") {
		t.Fatalf("member list missing entries:\n%s", mb)
	}
	if !strings.Contains(mb, "# 2 member") {
		t.Fatalf("expected exactly 2 deduped members:\n%s", mb)
	}

	// kind filter narrows to one type.
	if _, fb := do(t, ts, "GET", "/campaigns/"+cid+"/members?kind=contact", token, ""); !strings.Contains(fb, "Jane") || strings.Contains(fb, "Acme") {
		t.Fatalf("kind=contact should return only the contact:\n%s", fb)
	}

	// The detail view summarises counts (what a "fill N contacts" objective reads).
	if _, d := do(t, ts, "GET", "/campaigns/"+cid, token, ""); !strings.Contains(d, "contacts:") || !strings.Contains(d, "companies:") {
		t.Fatalf("campaign detail should show member counts:\n%s", d)
	}

	// Detach the contact.
	if s, _ := do(t, ts, "DELETE", "/campaigns/"+cid+"/members/contact/contact_"+contactID, token, ""); s != 200 {
		t.Fatalf("detach contact: %d", s)
	}
	if _, mb := do(t, ts, "GET", "/campaigns/"+cid+"/members", token, ""); strings.Contains(mb, "Jane") || !strings.Contains(mb, "# 1 member") {
		t.Fatalf("after detach only the company should remain:\n%s", mb)
	}

	// Attaching an unknown entity is a clear 400, not a silent dangling row.
	if s, _ := do(t, ts, "POST", "/campaigns/"+cid+"/members", token, `{"kind":"contact","id":"contact_zzzzz"}`); s != http.StatusBadRequest {
		t.Fatalf("attaching an unknown contact should 400, got %d", s)
	}
}

func TestCampaignAttachOnCreate(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	_, cb := do(t, ts, "POST", "/campaigns", token, `{"name":"Inbound"}`)
	cid := firstHandleID(cb, "campaign/")
	if cid == "" {
		t.Fatalf("no campaign handle: %s", cb)
	}

	// Creating a contact with ?campaign= attaches it in one call.
	if s, b := do(t, ts, "POST", "/contacts?campaign=campaign_"+cid+"&reason=inbound+lead", token,
		`{"name":"Jane","email":"jane@acme.com"}`); s != http.StatusCreated {
		t.Fatalf("create+attach contact: %d %q", s, b)
	}
	if _, mb := do(t, ts, "GET", "/campaigns/"+cid+"/members", token, ""); !strings.Contains(mb, "Jane") || !strings.Contains(mb, "inbound lead") {
		t.Fatalf("contact should be a member with its reason:\n%s", mb)
	}

	// Re-POSTing the same email upserts the existing contact and the attach stays
	// a single deduped membership.
	if s, _ := do(t, ts, "POST", "/contacts?campaign=campaign_"+cid, token,
		`{"name":"Jane Doe","email":"jane@acme.com"}`); s != http.StatusOK {
		t.Fatalf("upsert+attach should 200 (updated), got %d", s)
	}
	if _, mb := do(t, ts, "GET", "/campaigns/"+cid+"/members", token, ""); !strings.Contains(mb, "# 1 member") {
		t.Fatalf("upsert must not duplicate the membership:\n%s", mb)
	}

	// A company attaches the same way.
	if s, _ := do(t, ts, "POST", "/companies?campaign=campaign_"+cid, token, `{"name":"Acme","domain":"acme.com"}`); s != http.StatusCreated {
		t.Fatalf("create+attach company: %d", s)
	}
	if _, mb := do(t, ts, "GET", "/campaigns/"+cid+"/members", token, ""); !strings.Contains(mb, "Acme") {
		t.Fatalf("company should be a member:\n%s", mb)
	}

	// A bad campaign ref fails fast with a 400 and creates nothing.
	if s, _ := do(t, ts, "POST", "/contacts?campaign=campaign_zzzzz", token, `{"name":"Ghost","email":"ghost@x.com"}`); s != http.StatusBadRequest {
		t.Fatalf("unknown campaign ref should 400, got %d", s)
	}
	if _, lb := do(t, ts, "GET", "/contacts?search=Ghost", token, ""); strings.Contains(lb, "Ghost") {
		t.Fatalf("contact must not be created when the campaign ref is bad:\n%s", lb)
	}
}

func TestCampaignMembershipIsManyToMany(t *testing.T) {
	ts := newTestServer(t)
	token := authenticate(t, ts)

	// One contact can belong to two different campaigns at once.
	_, ab := do(t, ts, "POST", "/campaigns", token, `{"name":"Campaign A"}`)
	_, bb := do(t, ts, "POST", "/campaigns", token, `{"name":"Campaign B"}`)
	aID, bID := firstHandleID(ab, "campaign/"), firstHandleID(bb, "campaign/")
	_, ctb := do(t, ts, "POST", "/contacts", token, `{"name":"Shared","email":"shared@acme.com"}`)
	contactID := firstHandleID(ctb, "contact/")
	if aID == "" || bID == "" || contactID == "" {
		t.Fatalf("setup handles: a=%q b=%q contact=%q", aID, bID, contactID)
	}

	for _, camp := range []string{aID, bID} {
		if s, _ := do(t, ts, "POST", "/campaigns/"+camp+"/members", token,
			`{"kind":"contact","id":"contact_`+contactID+`"}`); s != 200 {
			t.Fatalf("attach to %s: %d", camp, s)
		}
	}
	for _, camp := range []string{aID, bID} {
		if _, mb := do(t, ts, "GET", "/campaigns/"+camp+"/members", token, ""); !strings.Contains(mb, "Shared") {
			t.Fatalf("contact should be a member of campaign %s:\n%s", camp, mb)
		}
	}
}
