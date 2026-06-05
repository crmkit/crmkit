package store

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/crmkit/crmkit/internal/protocol"
)

// PutEscalation stores (replacing any prior) a pending step-up challenge for a
// user to perform action on target. The caller supplies the code hash.
func (s *sqlStore) PutEscalation(userID, action, target, codeHash string, expiresAt time.Time) error {
	now := time.Now()
	_, err := s.exec(`
INSERT INTO escalations (id, user_id, action, target, code_hash, expires_at, attempts, created_at)
VALUES (?, ?, ?, ?, ?, ?, 0, ?)
ON CONFLICT(user_id, action, target) DO UPDATE SET
	code_hash = excluded.code_hash, expires_at = excluded.expires_at, attempts = 0, created_at = excluded.created_at`,
		protocol.NewID("esc"), userID, action, target, codeHash, unix(expiresAt), unix(now))
	return err
}

// VerifyEscalation checks the code hash for a pending challenge. On success it
// consumes the challenge and returns true. After 5 failed attempts the
// challenge is invalidated.
func (s *sqlStore) VerifyEscalation(userID, action, target, codeHash string, now time.Time) (bool, error) {
	var (
		storedHash string
		expiresAt  int64
		attempts   int
	)
	err := s.queryRow(`SELECT code_hash, expires_at, attempts FROM escalations WHERE user_id = ? AND action = ? AND target = ?`,
		userID, action, target).Scan(&storedHash, &expiresAt, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if now.Unix() > expiresAt || attempts >= 5 {
		_, _ = s.exec(`DELETE FROM escalations WHERE user_id = ? AND action = ? AND target = ?`, userID, action, target)
		return false, nil
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(codeHash)) != 1 {
		_, _ = s.exec(`UPDATE escalations SET attempts = attempts + 1 WHERE user_id = ? AND action = ? AND target = ?`,
			userID, action, target)
		return false, nil
	}

	_, _ = s.exec(`DELETE FROM escalations WHERE user_id = ? AND action = ? AND target = ?`, userID, action, target)
	return true, nil
}
