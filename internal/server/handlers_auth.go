package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crmkit/crmkit/internal/auth"
	"github.com/crmkit/crmkit/internal/render"
)

// handleRoot returns a short banner pointing agents at /help.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(fmt.Sprintf(`
crmkit - an agent-first CRM your AI drives over plain HTTP (headless, no UI).
Base URL: %s
Start here: GET %s/help

Not authenticated yet? POST /auth/request {"email":"you@example.com"} to begin.
`, s.cfg.Server.BaseURL, s.cfg.Server.BaseURL))
	render.Respond(w, r, http.StatusOK, map[string]string{
		"service":  "crmkit",
		"base_url": s.cfg.Server.BaseURL,
		"help":     s.cfg.Server.BaseURL + "/help",
	}, text)
}

// handleHelp returns the agent operating manual.
func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	render.Respond(w, r, http.StatusOK,
		map[string]string{"manual": Manual(s.cfg.Server.BaseURL)},
		Manual(s.cfg.Server.BaseURL))
}

// authRequestBody is the POST /auth/request payload.
type authRequestBody struct {
	Email string `json:"email"`
}

// handleAuthRequest generates a one-time login code and emails it.
func (s *Server) handleAuthRequest(w http.ResponseWriter, r *http.Request) {
	var body authRequestBody
	if err := decodeJSON(r, &body); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", `Send JSON: {"email":"you@example.com"}.`)
		return
	}
	email := auth.NormalizeEmail(body.Email)
	if !auth.ValidEmail(email) {
		render.Error(w, r, http.StatusBadRequest, "invalid_email", `Provide a valid email: {"email":"you@example.com"}.`)
		return
	}
	if !s.authLimiter.Allow("req:" + email) {
		w.Header().Set("Retry-After", "60")
		render.Error(w, r, http.StatusTooManyRequests, "rate_limited",
			"Too many login requests for this email. Wait before requesting another code.")
		return
	}

	code, err := auth.GenerateCode()
	if err != nil {
		render.Error(w, r, http.StatusInternalServerError, "server_error", "Could not start login. Try again.")
		return
	}
	expires := time.Now().Add(time.Duration(s.cfg.Server.OTPTTLSeconds) * time.Second)
	if err := s.store.PutOTP(email, auth.HashCode(s.cfg.Server.SecretKey, email, code), expires); err != nil {
		render.Error(w, r, http.StatusInternalServerError, "server_error", "Could not start login. Try again.")
		return
	}

	// In local mode, print the code to the console so you don't need email.
	if s.cfg.Server.Local {
		s.log.Info("login code (local)", slog.String("email", email), slog.String("code", code))
	}

	if err := s.mailer.Send(auth.LoginEmail(email, code, s.cfg.Server.OTPTTLSeconds/60)); err != nil {
		s.log.Error("login email send failed", slog.String("email", email), slog.String("error", err.Error()))
		render.Error(w, r, http.StatusInternalServerError, "email_failed", "Could not send the login email. Try again.")
		return
	}

	resp := map[string]any{
		"status":  "otp_sent",
		"email":   email,
		"message": "A login code was emailed. Ask the user for it, then POST /auth/verify {\"email\":\"...\",\"code\":\"123456\"}.",
	}
	text := fmt.Sprintf("OK otp_sent email=%s\nNext: ask the user for the 6-digit code, then POST /auth/verify {\"email\":%q,\"code\":\"<code>\"}.", email, email)

	// In local mode, echo the code so the agent can authenticate without email.
	if s.cfg.Server.Local {
		resp["local_code"] = code
		text += "\n# local mode: code=" + code
	}
	render.Respond(w, r, http.StatusOK, resp, text)
}

// authVerifyBody is the POST /auth/verify payload.
type authVerifyBody struct {
	Email     string `json:"email"`
	Code      string `json:"code"`
	TokenName string `json:"token_name,omitempty"`
}

// handleAuthVerify exchanges a valid login code for a long-lived API token.
func (s *Server) handleAuthVerify(w http.ResponseWriter, r *http.Request) {
	var body authVerifyBody
	if err := decodeJSON(r, &body); err != nil {
		render.Error(w, r, http.StatusBadRequest, "bad_request", `Send JSON: {"email":"...","code":"123456"}.`)
		return
	}
	email := auth.NormalizeEmail(body.Email)
	code := strings.TrimSpace(body.Code)
	if email == "" || code == "" {
		render.Error(w, r, http.StatusBadRequest, "bad_request", `Both "email" and "code" are required.`)
		return
	}
	if !s.authLimiter.Allow("verify:" + email) {
		w.Header().Set("Retry-After", "60")
		render.Error(w, r, http.StatusTooManyRequests, "rate_limited",
			"Too many verification attempts for this email. Wait before trying again.")
		return
	}

	ok, err := s.store.VerifyOTP(email, auth.HashCode(s.cfg.Server.SecretKey, email, code), time.Now())
	if err != nil {
		render.Error(w, r, http.StatusInternalServerError, "server_error", "Try again shortly.")
		return
	}
	if !ok {
		render.Error(w, r, http.StatusUnauthorized, "invalid_code",
			"Code is wrong or expired. Request a fresh one via POST /auth/request.")
		return
	}

	user, err := s.store.GetOrCreateIdentity(email)
	if err != nil {
		render.Error(w, r, http.StatusInternalServerError, "server_error", "Could not provision your workspace.")
		return
	}

	tokenName := strings.TrimSpace(body.TokenName)
	if tokenName == "" {
		tokenName = "default"
	}
	plaintext, hash, err := auth.GenerateToken()
	if err != nil {
		render.Error(w, r, http.StatusInternalServerError, "server_error", "Could not mint a token.")
		return
	}
	// The login token is scoped to the user's default workspace. To act in
	// another workspace they belong to, mint a token via POST /workspaces/{id}/tokens.
	tokenID, err := s.store.CreateToken(user.ID, user.DefaultWorkspaceID, tokenName, hash)
	if err != nil {
		render.Error(w, r, http.StatusInternalServerError, "server_error", "Could not store your token.")
		return
	}
	s.audit(sessionForUser(user.ID, user.DefaultWorkspaceID, tokenID), "auth.verify", "token/"+tokenID, "login")

	workspaces, _ := s.store.ListWorkspacesForUser(user.ID)

	resp := map[string]any{
		"status":       "authenticated",
		"token":        plaintext,
		"token_name":   tokenName,
		"workspace_id": user.DefaultWorkspaceID,
		"workspaces":   workspaces,
		"message":      "SAVE this token. Send it as `Authorization: Bearer <token>` on every later request.",
	}
	text := strings.TrimSpace(fmt.Sprintf(`
OK authenticated
token: %s
workspace: %s   (your default)

SAVE this token now. Reuse it on every request as the header:
  Authorization: Bearer %s
If you can store it in memory, do so; otherwise ask the user to keep it for next session.
%s`,
		plaintext, user.DefaultWorkspaceID, plaintext, render.WorkspacesHint(workspaces)))
	render.Respond(w, r, http.StatusOK, resp, text)
}

// handleWhoami reports the identity behind the current token.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	plan, usage, err := s.planSnapshot(sess)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	wsName, err := s.store.WorkspaceName(sess.WorkspaceID)
	if err != nil {
		s.serverErr(w, r)
		return
	}
	resp := map[string]any{
		"email":          sess.Email,
		"workspace_id":   sess.WorkspaceID,
		"workspace_name": wsName,
		"token_name":     sess.TokenName,
		"plan":           plan,
		"usage":          usage,
	}
	fields := []render.Field{
		render.F("email", sess.Email),
		render.F("workspace", sess.WorkspaceID),
		render.F("workspace_name", wsName),
		render.F("token", sess.TokenName),
		render.F("plan", plan),
	}
	for _, u := range usage {
		fields = append(fields, render.F(u.Resource, limitLabel(u.Used, u.Limit)))
	}
	render.Respond(w, r, http.StatusOK, resp, render.Record(fields...))
}
