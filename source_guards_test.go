package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// These are source-level guards rather than behavioural tests. They cover two
// defects that cannot be reached from a Go test - one lives in browser code,
// the other in a rollback path - but that are both trivially detectable in the
// source, and both of which shipped unnoticed.

const (
	sqliteMigrationDir = "db/db_sqlite3/migrations"
	mysqlMigrationDir  = "db/db_mysql/migrations"
)

// campaignResultsSources are the files the Campaign Results page is actually
// built from. templates/campaign_results.html loads the dist copy, so guarding
// only src would miss a stale build.
var campaignResultsSources = []string{
	"static/js/src/app/campaign_results.js",
	"static/js/dist/app/campaign_results.min.js",
}

var unguardedResultsLength = regexp.MustCompile(`campaign\.results\.length`)

var dropColumn = regexp.MustCompile(`(?i)DROP\s+COLUMN\s+\x60?([A-Za-z0-9_]+)\x60?`)

// TestCampaignResultsLengthIsGuarded fails if the Campaign Results page
// dereferences .length on campaign.results without a fallback.
//
// models.Campaign declares Results with `json:"results,omitempty"`, so a
// campaign that has no recipients is serialised with no "results" key at all.
// In the browser campaign.results is then undefined rather than an empty
// array, and reading .length off it throws a TypeError.
//
// It throws at a particularly bad moment: #loading has already been hidden and
// #campaignResults shown, but the results table has not been populated and no
// chart has been built yet. The operator is left with a permanently blank
// Results page, and because the same expression runs on the 60 second poll, it
// throws again on every tick and never recovers.
func TestCampaignResultsLengthIsGuarded(t *testing.T) {
	for _, src := range campaignResultsSources {
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("reading %s: %v", src, err)
		}
		offenders := []string{}
		for i, line := range strings.Split(string(body), "\n") {
			if unguardedResultsLength.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", src, i+1, strings.TrimSpace(line)))
			}
		}
		if len(offenders) > 0 {
			t.Errorf("campaign.results may be undefined; use (campaign.results || []).length:\n%s",
				strings.Join(offenders, "\n"))
		}
	}
}

// droppedColumns returns the column names a migration's Down section drops.
func droppedColumns(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	parts := strings.SplitN(string(body), "+goose Down", 2)
	if len(parts) != 2 {
		return nil
	}
	// Strip SQL comments first, so that prose explaining why a column is kept
	// does not itself read as a statement dropping it.
	statements := []string{}
	for _, line := range strings.Split(parts[1], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		statements = append(statements, line)
	}
	columns := []string{}
	for _, match := range dropColumn.FindAllStringSubmatch(strings.Join(statements, "\n"), -1) {
		columns = append(columns, strings.ToLower(match[1]))
	}
	sort.Strings(columns)
	return columns
}

// TestMigrationDownSectionsAgreeAcrossDialects fails if the same migration
// rolls back to a different schema on SQLite than it does on MySQL.
//
// The two dialects keep separate goose_db_version sequences but are meant to
// describe one schema. A Down section that drops columns on MySQL and leaves
// them in place on SQLite breaks that: rolling back and re-applying works on
// MySQL, while on SQLite the next Up hits "duplicate column name", models.Setup
// returns an error and gophish refuses to start. SQLite is the default driver,
// so that is the installation most likely to hit it.
//
// Dropping the columns on the SQLite side is not the fix. go.mod pins
// mattn/go-sqlite3 v2.0.3, roughly SQLite 3.30, and ALTER TABLE DROP COLUMN
// only arrived in 3.35. The repository's own convention for an ADD COLUMN
// migration - see 20180223101813_0.5.1_user_reporting and
// 20200914000000_0.11.0_last_login - is that the columns stay, in both
// dialects.
func TestMigrationDownSectionsAgreeAcrossDialects(t *testing.T) {
	sqliteFiles, err := filepath.Glob(filepath.Join(sqliteMigrationDir, "*.sql"))
	if err != nil {
		t.Fatalf("listing sqlite migrations: %v", err)
	}
	if len(sqliteFiles) == 0 {
		t.Fatalf("no migrations found under %s", sqliteMigrationDir)
	}
	for _, sqlitePath := range sqliteFiles {
		mysqlPath := filepath.Join(mysqlMigrationDir, filepath.Base(sqlitePath))
		if _, err := os.Stat(mysqlPath); err != nil {
			continue
		}
		sqliteColumns := droppedColumns(t, sqlitePath)
		mysqlColumns := droppedColumns(t, mysqlPath)
		if strings.Join(sqliteColumns, ",") != strings.Join(mysqlColumns, ",") {
			t.Errorf("%s rolls back to a different schema per dialect:\n  sqlite3 drops %v\n  mysql   drops %v",
				filepath.Base(sqlitePath), sqliteColumns, mysqlColumns)
		}
	}
}
