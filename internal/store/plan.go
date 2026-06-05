package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// WorkspacePlan returns the plan name assigned to a workspace.
func (s *sqlStore) WorkspacePlan(workspaceID string) (string, error) {
	var plan string
	err := s.queryRow(`SELECT plan FROM workspaces WHERE id = ?`, workspaceID).Scan(&plan)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return plan, err
}

// UserPlan returns the plan name assigned to a user.
func (s *sqlStore) UserPlan(userID string) (string, error) {
	var plan string
	err := s.queryRow(`SELECT plan FROM users WHERE id = ?`, userID).Scan(&plan)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return plan, err
}

// CountWorkspacesForUser counts the workspaces a user has created - the basis
// for the per-user MaxWorkspaces quota.
func (s *sqlStore) CountWorkspacesForUser(userID string) (int, error) {
	return s.countWhere(`SELECT count(*) FROM workspaces WHERE created_by = ?`, userID)
}

// CountResource counts existing objects of a kind within a workspace, for quota
// checks. kind is one of: members, invites, contacts, companies, deals. The
// table/condition are code-controlled (never user input), so this is
// injection-safe.
func (s *sqlStore) CountResource(workspaceID, kind string) (int, error) {
	switch kind {
	case "members":
		return s.countWhere(`SELECT count(*) FROM memberships WHERE workspace_id = ?`, workspaceID)
	case "invites":
		return s.countWhere(`SELECT count(*) FROM invites WHERE workspace_id = ?`, workspaceID)
	case "contacts":
		return s.countWhere(`SELECT count(*) FROM contacts WHERE workspace_id = ?`, workspaceID)
	case "companies":
		return s.countWhere(`SELECT count(*) FROM companies WHERE workspace_id = ?`, workspaceID)
	case "deals":
		return s.countWhere(`SELECT count(*) FROM deals WHERE workspace_id = ?`, workspaceID)
	default:
		return 0, fmt.Errorf("unknown resource %q", kind)
	}
}

// countWhere runs a count query and returns the scalar result.
func (s *sqlStore) countWhere(query string, args ...any) (int, error) {
	var n int
	if err := s.queryRow(query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
