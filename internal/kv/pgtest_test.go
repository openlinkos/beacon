package kv

// The PostgreSQL side of the suite: BEACON_TEST_POSTGRES_DSN, when set (CI's postgres service
// container, or a developer's docker run), runs the same storage tests on PostgreSQL. Each test
// gets a database of its own, created before the test and dropped after, so tests never see each
// other's rows the way the SQLite ones never share a file.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testDatabase makes a database of the test's own on the server the DSN names and returns the DSN
// that opens it. The drop happens when the test ends.
func testDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("BEACON_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BEACON_TEST_POSTGRES_DSN names the PostgreSQL server this suite also runs on")
	}
	if _, err := pgx.ParseConfig(dsn); err != nil {
		t.Fatalf("the test DSN is not valid")
	}
	raw := make([]byte, 4)
	_, _ = rand.Read(raw)
	name := "beacon_test_" + hex.EncodeToString(raw)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s`, name)); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = admin.ExecContext(dctx, fmt.Sprintf(`DROP DATABASE %s WITH (FORCE)`, name))
	})
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && u.Host != "" {
		u.Path = "/" + name
		return u.String()
	}
	// key=value form: replace the dbname it names, or add one
	for _, kv := range []string{"dbname", "database"} {
		if i := strings.Index(dsn, kv+"="); i >= 0 {
			rest := dsn[i+len(kv)+1:]
			if j := strings.IndexAny(rest, " \t"); j >= 0 {
				rest = rest[j:]
			} else {
				rest = ""
			}
			return dsn[:i] + kv + "=" + name + rest
		}
	}
	return dsn + " dbname=" + name
}

// openAny is the backend the storage contract runs on: SQLite, or PostgreSQL when the environment
// names one. The CI runs the suite twice, once each way.
func openAny(t *testing.T) SQL {
	t.Helper()
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") != "" {
		db, err := OpenPostgres(PostgresOptions{DSN: testDatabase(t)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db, _ := openSQLite(t)
	return db
}
