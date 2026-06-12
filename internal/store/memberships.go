package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// ErrLastAdmin is returned when removing a member would leave a workspace with
// no admin.
var ErrLastAdmin = errors.New("cannot remove the last admin")

// WorkspaceName returns the display name of a workspace, or ErrNotFound.
func (s *sqlStore) WorkspaceName(workspaceID string) (string, error) {
	var name string
	err := s.queryRow(`SELECT name FROM workspaces WHERE id = ?`, workspaceID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// ListWorkspacesForUser returns the workspaces a user belongs to, each with the
// user's role, newest first.
func (s *sqlStore) ListWorkspacesForUser(userID string) ([]protocol.Workspace, error) {
	rows, err := s.query(`
SELECT w.id, w.name, w.created_by, w.created_at, w.timezone, m.role
FROM memberships m JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = ? ORDER BY m.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Workspace
	for rows.Next() {
		var (
			ws        protocol.Workspace
			createdBy sql.NullString
			tz        sql.NullString
			createdAt int64
		)
		if err := rows.Scan(&ws.ID, &ws.Name, &createdBy, &createdAt, &tz, &ws.Role); err != nil {
			return nil, err
		}
		ws.CreatedBy = createdBy.String
		ws.Timezone = tz.String
		ws.CreatedAt = fromUnix(createdAt)
		out = append(out, ws)
	}
	return out, rows.Err()
}

// CreateWorkspace creates a workspace owned by userID (added as an admin member).
func (s *sqlStore) CreateWorkspace(userID, name string) (protocol.Workspace, error) {
	now := time.Now()
	ws := protocol.Workspace{
		ID:        protocol.NewID("ws"),
		Name:      name,
		CreatedBy: userID,
		CreatedAt: now,
		Timezone:  "UTC", // matches the column default; set via SetWorkspaceTimezone
		Role:      protocol.RoleAdmin,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return protocol.Workspace{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := s.txExec(tx, `INSERT INTO workspaces (id, name, created_by, created_at, plan) VALUES (?, ?, ?, ?, ?)`,
		ws.ID, ws.Name, userID, unix(now), s.planOrDefault()); err != nil {
		return protocol.Workspace{}, err
	}
	if _, err := s.txExec(tx, `INSERT INTO memberships (workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
		ws.ID, userID, protocol.RoleAdmin, unix(now)); err != nil {
		return protocol.Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Workspace{}, err
	}
	return ws, nil
}

// SetWorkspaceTimezone updates a workspace's display timezone (an IANA name,
// already validated by the caller). It does not touch any stored instant.
func (s *sqlStore) SetWorkspaceTimezone(workspaceID, tz string) error {
	res, err := s.exec(`UPDATE workspaces SET timezone = ? WHERE id = ?`, tz, workspaceID)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

// MemberRole returns the user's role in a workspace, or ErrNotFound if the user
// is not a member.
func (s *sqlStore) MemberRole(workspaceID, userID string) (string, error) {
	var role string
	err := s.queryRow(`SELECT role FROM memberships WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

// CreateInvite records (or updates the role of) a pending invite for an email to
// a workspace. If the email already maps to a member, that is surfaced by the
// caller; this simply stores the invite.
func (s *sqlStore) CreateInvite(workspaceID, email, role, invitedBy string) (protocol.Invite, error) {
	if role == "" {
		role = protocol.RoleMember
	}
	inv := protocol.Invite{
		ID:          protocol.NewID("inv"),
		WorkspaceID: workspaceID,
		Email:       email,
		Role:        role,
		InvitedBy:   invitedBy,
		CreatedAt:   time.Now(),
	}
	_, err := s.exec(`
INSERT INTO invites (id, workspace_id, email, role, invited_by, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(workspace_id, email) DO UPDATE SET role = excluded.role`,
		inv.ID, inv.WorkspaceID, inv.Email, inv.Role, inv.InvitedBy, unix(inv.CreatedAt))
	if err != nil {
		return protocol.Invite{}, err
	}
	return inv, nil
}

// ListMembers returns the members of a workspace with their emails and roles.
func (s *sqlStore) ListMembers(workspaceID string) ([]protocol.Member, error) {
	rows, err := s.query(`
SELECT m.user_id, u.email, m.role, m.created_at
FROM memberships m JOIN users u ON u.id = m.user_id
WHERE m.workspace_id = ? ORDER BY m.created_at ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Member
	for rows.Next() {
		var (
			mem     protocol.Member
			created int64
		)
		if err := rows.Scan(&mem.UserID, &mem.Email, &mem.Role, &created); err != nil {
			return nil, err
		}
		mem.CreatedAt = fromUnix(created)
		out = append(out, mem)
	}
	return out, rows.Err()
}

// ListInvites returns the pending invites for a workspace.
func (s *sqlStore) ListInvites(workspaceID string) ([]protocol.Invite, error) {
	rows, err := s.query(`
SELECT id, workspace_id, email, role, invited_by, created_at
FROM invites WHERE workspace_id = ? ORDER BY created_at ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Invite
	for rows.Next() {
		var (
			inv       protocol.Invite
			invitedBy sql.NullString
			created   int64
		)
		if err := rows.Scan(&inv.ID, &inv.WorkspaceID, &inv.Email, &inv.Role, &invitedBy, &created); err != nil {
			return nil, err
		}
		inv.InvitedBy = invitedBy.String
		inv.CreatedAt = fromUnix(created)
		out = append(out, inv)
	}
	return out, rows.Err()
}

// SetMemberRole changes a member's role. When demoting an admin it refuses to
// leave the workspace without an admin.
func (s *sqlStore) SetMemberRole(workspaceID, userID, role string) error {
	current, err := s.MemberRole(workspaceID, userID)
	if err != nil {
		return err
	}
	if current == protocol.RoleAdmin && role != protocol.RoleAdmin {
		var admins int
		if err := s.queryRow(`SELECT COUNT(*) FROM memberships WHERE workspace_id = ? AND role = ?`,
			workspaceID, protocol.RoleAdmin).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}
	res, err := s.exec(`UPDATE memberships SET role = ? WHERE workspace_id = ? AND user_id = ?`, role, workspaceID, userID)
	if err != nil {
		return err
	}
	return affectedOne(res)
}

// DeleteWorkspace removes a workspace and everything scoped to it (CRM data,
// memberships, invites, tokens, audit). Users whose default workspace was the
// deleted one have their default cleared; it is re-provisioned on next login.
func (s *sqlStore) DeleteWorkspace(workspaceID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	for _, stmt := range []string{
		`DELETE FROM contacts WHERE workspace_id = ?`,
		`DELETE FROM companies WHERE workspace_id = ?`,
		`DELETE FROM deals WHERE workspace_id = ?`,
		`DELETE FROM activities WHERE workspace_id = ?`,
		`DELETE FROM audit_log WHERE workspace_id = ?`,
		`DELETE FROM tokens WHERE workspace_id = ?`,
		`DELETE FROM invites WHERE workspace_id = ?`,
		`DELETE FROM memberships WHERE workspace_id = ?`,
		`UPDATE users SET default_workspace_id = NULL WHERE default_workspace_id = ?`,
	} {
		if _, err := s.txExec(tx, stmt, workspaceID); err != nil {
			return err
		}
	}
	res, err := s.txExec(tx, `DELETE FROM workspaces WHERE id = ?`, workspaceID)
	if err != nil {
		return err
	}
	if err := affectedOne(res); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveMember removes a user's membership from a workspace. It refuses to
// remove the last remaining admin.
func (s *sqlStore) RemoveMember(workspaceID, userID string) error {
	role, err := s.MemberRole(workspaceID, userID)
	if err != nil {
		return err
	}
	if role == protocol.RoleAdmin {
		var admins int
		if err := s.queryRow(`SELECT COUNT(*) FROM memberships WHERE workspace_id = ? AND role = ?`,
			workspaceID, protocol.RoleAdmin).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}
	res, err := s.exec(`DELETE FROM memberships WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID)
	if err != nil {
		return err
	}
	return affectedOne(res)
}
