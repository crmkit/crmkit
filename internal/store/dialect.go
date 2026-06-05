package store

import (
	"database/sql"
	"strconv"
	"strings"
)

// placeholderStyle is how a backend marks bind parameters.
type placeholderStyle int

const (
	placeholderQuestion placeholderStyle = iota // ?      (SQLite)
	placeholderDollar                           // $1, $2 (Postgres)
)

// dialect captures the small differences between backends. All queries are
// written once with "?" placeholders and case-insensitive search; the dialect
// rebinds placeholders and picks the right LIKE keyword so the same SQL runs on
// both SQLite and Postgres.
type dialect struct {
	driver      string // database/sql driver name
	placeholder placeholderStyle
	like        string // "LIKE" (SQLite is case-insensitive) or "ILIKE" (Postgres)
}

var (
	sqliteDialect   = dialect{driver: "sqlite", placeholder: placeholderQuestion, like: "LIKE"}
	postgresDialect = dialect{driver: "pgx", placeholder: placeholderDollar, like: "ILIKE"}
)

// rebind converts "?" placeholders to the dialect's form. For SQLite it is a
// no-op; for Postgres it numbers them $1, $2, … in order of appearance. Our SQL
// never contains a literal "?", so a straight scan is safe.
func (d dialect) rebind(q string) string {
	if d.placeholder != placeholderDollar {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

// The exec/query helpers route every statement through rebind so the
// ?-placeholder SQL written once works on both backends.

func (s *sqlStore) exec(q string, args ...any) (sql.Result, error) {
	return s.db.Exec(s.d.rebind(q), args...)
}

func (s *sqlStore) query(q string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.d.rebind(q), args...)
}

func (s *sqlStore) queryRow(q string, args ...any) *sql.Row {
	return s.db.QueryRow(s.d.rebind(q), args...)
}

func (s *sqlStore) txExec(tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	return tx.Exec(s.d.rebind(q), args...)
}
