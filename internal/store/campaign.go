package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// ---- campaigns -----------------------------------------------------------

const campaignColumns = `id, handle, version, name, description, status, created_at, updated_at, created_by`

// CreateCampaign inserts a campaign. ID/timestamps/handle are assigned if unset.
func (s *sqlStore) CreateCampaign(ws string, c *protocol.Campaign) error {
	now := time.Now()
	if c.ID == "" {
		c.ID = protocol.NewID("cmp")
	}
	if c.Status == "" {
		c.Status = "active"
	}
	c.CreatedAt, c.UpdatedAt = now, now
	c.Version = 1
	var err error
	c.Handle, err = s.genHandle(func(handle string) error {
		_, e := s.exec(`
INSERT INTO campaigns (id, workspace_id, handle, name, description, status, created_at, updated_at, created_by)
VALUES (?,?,?,?,?,?,?,?,?)`,
			c.ID, ws, handle, c.Name, c.Description, c.Status, unix(now), unix(now), c.CreatedBy)
		return e
	})
	return err
}

// GetCampaign loads one campaign scoped to the workspace.
func (s *sqlStore) GetCampaign(ws, id string) (protocol.Campaign, error) {
	row := s.queryRow(`SELECT `+campaignColumns+` FROM campaigns WHERE workspace_id = ? AND id = ?`, ws, id)
	return scanCampaign(row)
}

// UpdateCampaign overwrites mutable campaign fields, bumps updated_at, and
// increments version. See UpdateContact for the ifMatch semantics.
func (s *sqlStore) UpdateCampaign(ws string, c *protocol.Campaign, ifMatch int64) error {
	c.UpdatedAt = time.Now()
	res, err := s.exec(`
UPDATE campaigns SET name=?, description=?, status=?, updated_at=?, version = version + 1
WHERE workspace_id = ? AND id = ? AND (? <= 0 OR version = ?)`,
		c.Name, c.Description, c.Status, unix(c.UpdatedAt), ws, c.ID, ifMatch, ifMatch)
	if err != nil {
		return err
	}
	if err := s.checkConditional(res, ws, "campaigns", c.ID, ifMatch); err != nil {
		return err
	}
	return s.reloadVersion(ws, "campaigns", c.ID, &c.Version)
}

// DeleteCampaign removes a campaign and its membership rows. The members
// (contacts/companies) themselves are untouched - only the associations go.
func (s *sqlStore) DeleteCampaign(ws, id string) error {
	if _, err := s.exec(`DELETE FROM campaign_members WHERE workspace_id = ? AND campaign_id = ?`, ws, id); err != nil {
		return err
	}
	res, err := s.exec(`DELETE FROM campaigns WHERE workspace_id = ? AND id = ?`, ws, id)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

func scanCampaign(sc scanner) (protocol.Campaign, error) {
	var (
		c                   protocol.Campaign
		description, status sql.NullString
		createdBy           sql.NullString
		created, upd        int64
	)
	err := sc.Scan(&c.ID, &c.Handle, &c.Version, &c.Name, &description, &status, &created, &upd, &createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Campaign{}, ErrNotFound
	}
	if err != nil {
		return protocol.Campaign{}, err
	}
	c.Description, c.Status = description.String, status.String
	c.CreatedAt, c.UpdatedAt = fromUnix(created), fromUnix(upd)
	c.CreatedBy = createdBy.String
	return c, nil
}

// ---- campaign membership -------------------------------------------------

// AttachCampaignMember associates a contact or company with a campaign. It is
// idempotent: re-attaching an entity already in the campaign is a no-op (the
// original provenance is kept), so an agent re-discovering the same entity costs
// nothing and never double-counts toward an objective. Both ids are assumed
// already workspace-validated by the caller.
func (s *sqlStore) AttachCampaignMember(ws, campaignID, kind, entityID, reason, addedBy string) error {
	_, err := s.exec(`
INSERT INTO campaign_members (campaign_id, workspace_id, entity_kind, entity_id, reason, added_by, added_at)
VALUES (?,?,?,?,?,?,?)`,
		campaignID, ws, kind, entityID, reason, addedBy, unix(time.Now()))
	if isUniqueViolation(err) {
		return nil // already a member - idempotent
	}
	return err
}

// DetachCampaignMember removes one entity's association with a campaign.
func (s *sqlStore) DetachCampaignMember(ws, campaignID, kind, entityID string) error {
	res, err := s.exec(`DELETE FROM campaign_members
WHERE workspace_id = ? AND campaign_id = ? AND entity_kind = ? AND entity_id = ?`,
		ws, campaignID, kind, entityID)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

// ListCampaignMembers returns the entities gathered under a campaign, newest first.
// kind ("" = all) optionally restricts to one entity kind. Members whose entity
// has since been deleted (dangling rows) are filtered out rather than shown.
func (s *sqlStore) ListCampaignMembers(ws, campaignID, kind string, limit int) ([]protocol.CampaignMember, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT entity_kind, entity_id, reason, added_by, added_at FROM campaign_members
WHERE workspace_id = ? AND campaign_id = ?`
	args := []any{ws, campaignID}
	if kind != "" {
		q += ` AND entity_kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY added_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	out := []protocol.CampaignMember{}
	byKind := map[string][]string{} // kind -> entity ids, for batch name/handle resolution
	for rows.Next() {
		var (
			m       protocol.CampaignMember
			reason  sql.NullString
			addedBy sql.NullString
			addedAt int64
		)
		if err := rows.Scan(&m.Kind, &m.ID, &reason, &addedBy, &addedAt); err != nil {
			rows.Close()
			return nil, err
		}
		m.Reason, m.AddedBy = reason.String, addedBy.String
		m.AddedAt = fromUnix(addedAt)
		out = append(out, m)
		byKind[m.Kind] = append(byKind[m.Kind], m.ID)
	}
	err = rows.Err()
	rows.Close() // close before the resolution queries (single-connection safety)
	if err != nil {
		return nil, err
	}

	// Resolve display names + handles per kind, then drop dangling rows.
	names := map[string]string{}
	handles := map[string]string{}
	for k, ids := range byKind {
		table, ok := handleTable[k]
		if !ok {
			continue
		}
		ns, err := s.namesByID(ws, table, ids)
		if err != nil {
			return nil, err
		}
		hs, err := s.handlesByID(ws, table, ids)
		if err != nil {
			return nil, err
		}
		for id, n := range ns {
			names[k+"/"+id] = n
		}
		for id, h := range hs {
			handles[k+"/"+id] = h
		}
	}
	live := out[:0]
	for _, m := range out {
		key := m.Kind + "/" + m.ID
		h, ok := handles[key]
		if !ok || h == "" {
			continue // entity since deleted - skip the dangling association
		}
		m.Handle = h
		m.Name = names[key]
		live = append(live, m)
	}
	return live, nil
}

// CountCampaignMembers returns a campaign's membership counts by kind (e.g.
// {"contact":47,"company":12}). Dangling rows are not filtered here - this is the
// raw association count, used for the at-a-glance objective progress.
func (s *sqlStore) CountCampaignMembers(ws, campaignID string) (map[string]int, error) {
	rows, err := s.query(`SELECT entity_kind, count(*) FROM campaign_members
WHERE workspace_id = ? AND campaign_id = ? GROUP BY entity_kind`, ws, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}
