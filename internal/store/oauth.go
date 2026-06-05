package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// OAuthClient is a dynamically-registered MCP/OAuth client (RFC 7591). crmkit
// only supports public clients (PKCE, no client secret), so the registration
// holds just the client id and its permitted redirect URIs.
type OAuthClient struct {
	ID           string
	RedirectURIs []string
	Name         string
	CreatedAt    time.Time
}

// AuthCode is the data bound to an issued OAuth authorization code, returned
// when the code is consumed at the token endpoint. The code itself is stored
// only as a hash; this is the associated grant.
type AuthCode struct {
	ClientID      string
	UserID        string
	WorkspaceID   string
	RedirectURI   string
	CodeChallenge string
	Scope         string
}

// RegisterOAuthClient stores a new public client and returns its generated
// client id. redirectURIs must already be validated against the configured
// allowlist by the caller.
func (s *sqlStore) RegisterOAuthClient(redirectURIs []string, name string) (string, error) {
	id := protocol.NewID("mcpc")
	uris, err := json.Marshal(redirectURIs)
	if err != nil {
		return "", err
	}
	if _, err := s.exec(`INSERT INTO oauth_clients (id, redirect_uris, client_name, created_at) VALUES (?, ?, ?, ?)`,
		id, string(uris), name, unix(time.Now())); err != nil {
		return "", err
	}
	return id, nil
}

// GetOAuthClient loads a registered client by id, returning ErrNotFound when it
// is unknown.
func (s *sqlStore) GetOAuthClient(clientID string) (OAuthClient, error) {
	var (
		c         OAuthClient
		uris      string
		name      sql.NullString
		createdAt int64
	)
	err := s.queryRow(`SELECT id, redirect_uris, client_name, created_at FROM oauth_clients WHERE id = ?`, clientID).
		Scan(&c.ID, &uris, &name, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClient{}, ErrNotFound
	}
	if err != nil {
		return OAuthClient{}, err
	}
	_ = json.Unmarshal([]byte(uris), &c.RedirectURIs)
	c.Name = name.String
	c.CreatedAt = fromUnix(createdAt)
	return c, nil
}

// PutAuthCode stores a pending authorization code (by hash) and the grant it
// represents. Codes are single-use and expire at expiresAt.
func (s *sqlStore) PutAuthCode(codeHash string, g AuthCode, expiresAt time.Time) error {
	_, err := s.exec(`
INSERT INTO oauth_codes (code_hash, client_id, user_id, workspace_id, redirect_uri, code_challenge, scope, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		codeHash, g.ClientID, g.UserID, g.WorkspaceID, g.RedirectURI, g.CodeChallenge, g.Scope,
		unix(expiresAt), unix(time.Now()))
	return err
}

// ConsumeAuthCode atomically deletes an authorization code by hash and returns
// its grant (DELETE ... RETURNING, so two concurrent token requests cannot both
// consume the same code). An unknown or expired code yields ErrNotFound; an
// expired row is still deleted by the DELETE so it cannot be retried.
func (s *sqlStore) ConsumeAuthCode(codeHash string, now time.Time) (AuthCode, error) {
	var (
		g         AuthCode
		scope     sql.NullString
		expiresAt int64
	)
	err := s.queryRow(`DELETE FROM oauth_codes WHERE code_hash = ? RETURNING client_id, user_id, workspace_id, redirect_uri, code_challenge, scope, expires_at`, codeHash).
		Scan(&g.ClientID, &g.UserID, &g.WorkspaceID, &g.RedirectURI, &g.CodeChallenge, &scope, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthCode{}, ErrNotFound
	}
	if err != nil {
		return AuthCode{}, err
	}
	g.Scope = scope.String
	if now.Unix() > expiresAt {
		return AuthCode{}, ErrNotFound
	}
	return g, nil
}

// RefreshGrant is the identity bound to an OAuth refresh token. AccessTokenHash
// is the hash of the access token issued alongside this refresh token, so that
// rotating the refresh token can revoke the access token it supersedes.
type RefreshGrant struct {
	ClientID        string
	UserID          string
	WorkspaceID     string
	Scope           string
	AccessTokenHash string
}

// PutRefreshToken stores a refresh token (by hash) and the identity it renews,
// including the paired access token's hash.
func (s *sqlStore) PutRefreshToken(tokenHash string, g RefreshGrant, expiresAt time.Time) error {
	_, err := s.exec(`
INSERT INTO oauth_refresh_tokens (token_hash, client_id, user_id, workspace_id, scope, access_token_hash, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, g.ClientID, g.UserID, g.WorkspaceID, g.Scope, g.AccessTokenHash, unix(expiresAt), unix(time.Now()))
	return err
}

// ConsumeRefreshToken atomically deletes a refresh token by hash and returns its
// grant (DELETE ... RETURNING: rotation is single-use and race-free). Unknown or
// expired tokens yield ErrNotFound; expired rows are still deleted.
func (s *sqlStore) ConsumeRefreshToken(tokenHash string, now time.Time) (RefreshGrant, error) {
	var (
		g          RefreshGrant
		scope      sql.NullString
		accessHash sql.NullString
		expiresAt  int64
	)
	err := s.queryRow(`DELETE FROM oauth_refresh_tokens WHERE token_hash = ? RETURNING client_id, user_id, workspace_id, scope, access_token_hash, expires_at`, tokenHash).
		Scan(&g.ClientID, &g.UserID, &g.WorkspaceID, &scope, &accessHash, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RefreshGrant{}, ErrNotFound
	}
	if err != nil {
		return RefreshGrant{}, err
	}
	g.Scope = scope.String
	g.AccessTokenHash = accessHash.String
	if now.Unix() > expiresAt {
		return RefreshGrant{}, ErrNotFound
	}
	return g, nil
}

// RevokeRefreshTokenByHash deletes a refresh token (the OAuth revocation
// endpoint). Unknown tokens are a no-op.
func (s *sqlStore) RevokeRefreshTokenByHash(tokenHash string) error {
	_, err := s.exec(`DELETE FROM oauth_refresh_tokens WHERE token_hash = ?`, tokenHash)
	return err
}

// RevokeTokenByHash revokes the token matching tokenHash (the OAuth revocation
// endpoint). Unknown or already-revoked tokens are a no-op, per RFC 7009 which
// requires the endpoint to succeed regardless.
func (s *sqlStore) RevokeTokenByHash(tokenHash string) error {
	_, err := s.exec(`UPDATE tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`,
		unix(time.Now()), tokenHash)
	return err
}
