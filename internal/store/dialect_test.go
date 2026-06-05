package store

import "testing"

func TestRebind(t *testing.T) {
	cases := []struct {
		d    dialect
		in   string
		want string
	}{
		{sqliteDialect, "SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = ? AND b = ?"},
		{postgresDialect, "SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2"},
		{postgresDialect, "INSERT INTO t VALUES (?, ?, ?)", "INSERT INTO t VALUES ($1, $2, $3)"},
		{postgresDialect, "no params here", "no params here"},
	}
	for _, c := range cases {
		if got := c.d.rebind(c.in); got != c.want {
			t.Errorf("%s rebind(%q) = %q, want %q", c.d.driver, c.in, got, c.want)
		}
	}
}

func TestDialectLikeKeyword(t *testing.T) {
	if sqliteDialect.like != "LIKE" {
		t.Errorf("sqlite like = %q", sqliteDialect.like)
	}
	if postgresDialect.like != "ILIKE" {
		t.Errorf("postgres like = %q", postgresDialect.like)
	}
}
