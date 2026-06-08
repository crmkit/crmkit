package server

import (
	"fmt"
	"net/http"

	"github.com/crmkit/crmkit/internal/protocol"
	"github.com/crmkit/crmkit/internal/render"
)

// ResourceUsage is one resource's current count against its plan limit
// (-1 = unlimited), reported by /whoami.
type ResourceUsage struct {
	Resource string `json:"resource"`
	Used     int    `json:"used"`
	Limit    int    `json:"limit"`
}

// planSnapshot gathers the workspace plan and current usage for a session: the
// workspace-scoped resources plus the user's workspace count. Used by /whoami so
// the agent knows its ceilings.
func (s *Server) planSnapshot(sess protocol.Session) (plan string, usage []ResourceUsage, err error) {
	plan, err = s.store.WorkspacePlan(sess.WorkspaceID)
	if err != nil {
		return "", nil, err
	}
	wl := s.cfg.Plans.LimitsFor(plan)
	for _, res := range []string{"contacts", "companies", "deals", "tickets", "activities", "members"} {
		used, e := s.workspaceResourceCount(sess.WorkspaceID, res)
		if e != nil {
			return "", nil, e
		}
		usage = append(usage, ResourceUsage{res, used, wl.For(res)})
	}
	// Workspaces are governed by the user's (not workspace's) plan.
	userPlan, err := s.store.UserPlan(sess.UserID)
	if err != nil {
		return "", nil, err
	}
	wsCount, err := s.store.CountWorkspacesForUser(sess.UserID)
	if err != nil {
		return "", nil, err
	}
	usage = append(usage, ResourceUsage{"workspaces", wsCount, s.cfg.Plans.LimitsFor(userPlan).MaxWorkspaces})
	return plan, usage, nil
}

// limitLabel formats "used / limit", or "used / unlimited" for an unlimited cap.
func limitLabel(used, limit int) string {
	if limit < 0 {
		return fmt.Sprintf("%d / unlimited", used)
	}
	return fmt.Sprintf("%d / %d", used, limit)
}

// enforceWorkspaceQuota checks the workspace plan's limit for a resource
// ("members", "contacts", "companies", "deals"). If the limit is reached it
// writes a plan_limit_reached error and returns false (the caller should
// return); true means the create may proceed. Unlimited (-1) always passes.
func (s *Server) enforceWorkspaceQuota(w http.ResponseWriter, r *http.Request, workspaceID, resource string) bool {
	plan, err := s.store.WorkspacePlan(workspaceID)
	if err != nil {
		s.serverErr(w, r)
		return false
	}
	limit := s.cfg.Plans.LimitsFor(plan).For(resource)
	if limit < 0 {
		return true
	}
	used, err := s.workspaceResourceCount(workspaceID, resource)
	if err != nil {
		s.serverErr(w, r)
		return false
	}
	if used >= limit {
		s.writePlanLimit(w, r, plan, resource, limit, used)
		return false
	}
	return true
}

// enforceWorkspaceCreateQuota checks the user plan's MaxWorkspaces before
// creating a new workspace. Same contract as enforceWorkspaceQuota.
func (s *Server) enforceWorkspaceCreateQuota(w http.ResponseWriter, r *http.Request, userID string) bool {
	plan, err := s.store.UserPlan(userID)
	if err != nil {
		s.serverErr(w, r)
		return false
	}
	limit := s.cfg.Plans.LimitsFor(plan).MaxWorkspaces
	if limit < 0 {
		return true
	}
	used, err := s.store.CountWorkspacesForUser(userID)
	if err != nil {
		s.serverErr(w, r)
		return false
	}
	if used >= limit {
		s.writePlanLimit(w, r, plan, "workspaces", limit, used)
		return false
	}
	return true
}

// canCreateWorkspace reports whether userID is under their workspace quota. It is
// the non-HTTP counterpart of enforceWorkspaceCreateQuota, for flows (e.g. the
// OAuth workspace picker) that render their own page instead of a JSON error.
func (s *Server) canCreateWorkspace(userID string) (bool, error) {
	plan, err := s.store.UserPlan(userID)
	if err != nil {
		return false, err
	}
	limit := s.cfg.Plans.LimitsFor(plan).MaxWorkspaces
	if limit < 0 {
		return true, nil
	}
	used, err := s.store.CountWorkspacesForUser(userID)
	if err != nil {
		return false, err
	}
	return used < limit, nil
}

// workspaceResourceCount returns current usage for a resource. A member "seat"
// is a member plus any pending invite, so over-inviting is rejected.
func (s *Server) workspaceResourceCount(workspaceID, resource string) (int, error) {
	if resource == "members" {
		members, err := s.store.CountResource(workspaceID, "members")
		if err != nil {
			return 0, err
		}
		invites, err := s.store.CountResource(workspaceID, "invites")
		if err != nil {
			return 0, err
		}
		return members + invites, nil
	}
	return s.store.CountResource(workspaceID, resource)
}

func (s *Server) writePlanLimit(w http.ResponseWriter, r *http.Request, plan, resource string, limit, used int) {
	render.Error(w, r, http.StatusForbidden, "plan_limit_reached",
		fmt.Sprintf("Plan %q allows at most %d %s (currently %d). Ask a workspace admin to raise the limit or change the plan.",
			plan, limit, resource, used))
}
