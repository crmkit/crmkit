package store

import (
	"testing"
	"time"
)

func TestOAuthClientRoundTrip(t *testing.T) {
	st := newTestStore(t)

	id, err := st.RegisterOAuthClient([]string{"https://a.example/cb", "https://b.example/cb"}, "My Client")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if id == "" {
		t.Fatal("empty client id")
	}

	c, err := st.GetOAuthClient(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c.Name != "My Client" || len(c.RedirectURIs) != 2 || c.RedirectURIs[0] != "https://a.example/cb" {
		t.Fatalf("unexpected client: %+v", c)
	}

	if _, err := st.GetOAuthClient("mcpc_missing"); err != ErrNotFound {
		t.Fatalf("missing client should be ErrNotFound, got %v", err)
	}
}

func TestAuthCodeSingleUseAndExpiry(t *testing.T) {
	st := newTestStore(t)
	now := time.Now()
	grant := AuthCode{ClientID: "c1", UserID: "u1", WorkspaceID: "w1", RedirectURI: "https://x/cb", CodeChallenge: "chal", Scope: "crm"}

	// Valid code consumes once and returns the grant.
	if err := st.PutAuthCode("hash-valid", grant, now.Add(time.Minute)); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := st.ConsumeAuthCode("hash-valid", now)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got.ClientID != "c1" || got.WorkspaceID != "w1" || got.CodeChallenge != "chal" || got.Scope != "crm" {
		t.Fatalf("unexpected grant: %+v", got)
	}
	// Single-use: the second consume finds nothing.
	if _, err := st.ConsumeAuthCode("hash-valid", now); err != ErrNotFound {
		t.Fatalf("code should be single-use, got %v", err)
	}
	// Unknown code.
	if _, err := st.ConsumeAuthCode("nope", now); err != ErrNotFound {
		t.Fatalf("unknown code should be ErrNotFound, got %v", err)
	}

	// Expired code: ErrNotFound, and the row is removed (cannot be retried).
	if err := st.PutAuthCode("hash-exp", grant, now.Add(-time.Second)); err != nil {
		t.Fatalf("put expired: %v", err)
	}
	if _, err := st.ConsumeAuthCode("hash-exp", now); err != ErrNotFound {
		t.Fatalf("expired code should be ErrNotFound, got %v", err)
	}
}

func TestRefreshTokenRoundTripAndRotation(t *testing.T) {
	st := newTestStore(t)
	now := time.Now()
	grant := RefreshGrant{ClientID: "c1", UserID: "u1", WorkspaceID: "w1", Scope: "crm", AccessTokenHash: "acc-hash"}

	if err := st.PutRefreshToken("rt-1", grant, now.Add(time.Hour)); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := st.ConsumeRefreshToken("rt-1", now)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got.ClientID != "c1" || got.AccessTokenHash != "acc-hash" || got.Scope != "crm" {
		t.Fatalf("unexpected grant: %+v", got)
	}
	// Rotation: single-use.
	if _, err := st.ConsumeRefreshToken("rt-1", now); err != ErrNotFound {
		t.Fatalf("refresh token should be single-use, got %v", err)
	}

	// Expired refresh token.
	if err := st.PutRefreshToken("rt-exp", grant, now.Add(-time.Second)); err != nil {
		t.Fatalf("put expired: %v", err)
	}
	if _, err := st.ConsumeRefreshToken("rt-exp", now); err != ErrNotFound {
		t.Fatalf("expired refresh should be ErrNotFound, got %v", err)
	}
}

func TestRevokeByHash(t *testing.T) {
	st := newTestStore(t)

	// A real access token row, then revoke it by hash.
	user, err := st.GetOrCreateIdentity("rev@example.com")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if _, err := st.CreateToken(user.ID, user.DefaultWorkspaceID, "t", "tok-hash"); err != nil {
		t.Fatalf("create token: %v", err)
	}
	if _, err := st.ResolveToken("tok-hash"); err != nil {
		t.Fatalf("token should resolve before revoke: %v", err)
	}
	if err := st.RevokeTokenByHash("tok-hash"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := st.ResolveToken("tok-hash"); err != ErrNotFound {
		t.Fatalf("revoked token should not resolve, got %v", err)
	}
	// Revoking an unknown hash is a no-op (no error).
	if err := st.RevokeTokenByHash("unknown"); err != nil {
		t.Fatalf("revoking unknown should be a no-op, got %v", err)
	}

	// Refresh-token revoke deletes the row.
	if err := st.PutRefreshToken("rt-x", RefreshGrant{ClientID: "c", UserID: user.ID, WorkspaceID: user.DefaultWorkspaceID}, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("put refresh: %v", err)
	}
	if err := st.RevokeRefreshTokenByHash("rt-x"); err != nil {
		t.Fatalf("revoke refresh: %v", err)
	}
	if _, err := st.ConsumeRefreshToken("rt-x", time.Now()); err != ErrNotFound {
		t.Fatalf("revoked refresh should be gone, got %v", err)
	}
}
