package models

import (
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

// TestResolveDSNLeavesNonMySQLAlone - sqlite3 takes a file path, not a DSN, and
// must never be rewritten.
func TestResolveDSNLeavesNonMySQLAlone(t *testing.T) {
	for _, path := range []string{"gophish.db", ":memory:", "/var/lib/gophish/gophish.db"} {
		got, err := resolveDSN("sqlite3", path)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", path, err)
		}
		if got != path {
			t.Fatalf("sqlite3 path was rewritten. expected %q got %q", path, got)
		}
	}
}

// TestResolveDSNAddsZeroDateModes covers the reason this exists: gophish writes
// the zero time.Time for "has not happened yet", and MySQL 8 rejects
// '0000-00-00' under its default sql_mode.
func TestResolveDSNAddsZeroDateModes(t *testing.T) {
	in := "gophish:secret@(db.example.com:3306)/gophish?charset=utf8&parseTime=True&loc=Local"
	got, err := resolveDSN("mysql", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := mysql.ParseDSN(got)
	if err != nil {
		t.Fatalf("produced an unparseable DSN %q: %v", got, err)
	}
	if cfg.Params["sql_mode"] != mysqlRelaxedZeroDateModes {
		t.Fatalf("unexpected sql_mode. expected %q got %q",
			mysqlRelaxedZeroDateModes, cfg.Params["sql_mode"])
	}

	// The zero-date restrictions have to be gone...
	for _, mode := range []string{"NO_ZERO_DATE", "NO_ZERO_IN_DATE"} {
		if strings.Contains(cfg.Params["sql_mode"], mode) {
			t.Fatalf("%s must not be present, it is what rejects '0000-00-00'", mode)
		}
	}
	// ...but nothing else may be loosened. Bad data should still be refused.
	if !strings.Contains(cfg.Params["sql_mode"], "STRICT_TRANS_TABLES") {
		t.Fatalf("STRICT_TRANS_TABLES was dropped; only the zero-date modes should be")
	}

	// Everything the operator configured has to survive.
	if cfg.User != "gophish" || cfg.Passwd != "secret" {
		t.Fatalf("credentials were mangled: user=%q", cfg.User)
	}
	if cfg.Addr != "db.example.com:3306" || cfg.DBName != "gophish" {
		t.Fatalf("address or database was mangled: addr=%q db=%q", cfg.Addr, cfg.DBName)
	}
	if !cfg.ParseTime {
		t.Fatalf("parseTime was dropped")
	}
}

// TestResolveDSNRespectsOperatorSQLMode - an operator who has set sql_mode has
// made a deliberate choice and must be left alone.
func TestResolveDSNRespectsOperatorSQLMode(t *testing.T) {
	in := "gophish:secret@(db.example.com:3306)/gophish?parseTime=True&sql_mode=%27TRADITIONAL%27"
	got, err := resolveDSN("mysql", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != in {
		t.Fatalf("an explicitly configured sql_mode was overwritten.\n  in:  %s\n  out: %s", in, got)
	}
}

// TestResolveDSNRejectsGarbage - a malformed connection string should be
// reported rather than silently passed through to the driver.
func TestResolveDSNRejectsGarbage(t *testing.T) {
	if _, err := resolveDSN("mysql", "this is not a dsn"); err == nil {
		t.Fatalf("expected an error for a malformed DSN")
	}
}
