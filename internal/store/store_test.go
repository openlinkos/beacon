package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// currentDSN is the PostgreSQL database the running test's open(t) opened, empty on SQLite: what
// reopen needs to reach the same state. The package's tests run one at a time.
var currentDSN string

// open is the store on the backend the environment picks: SQLite by default, or PostgreSQL when
// BEACON_TEST_POSTGRES_DSN names one (CI runs the whole suite on both). The path it returns is the
// SQLite file, which the PostgreSQL runs do not use.
func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "status.db")
	if dsn := os.Getenv("BEACON_TEST_POSTGRES_DSN"); dsn != "" {
		currentDSN = testDatabase(t, dsn)
		s, err := OpenWith(Options{Driver: "postgres", DSN: currentDSN})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s, path
	}
	currentDSN = ""
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// reopen opens the database open(t) opened, as a restart of the service would.
func reopen(t *testing.T, path string) (*Store, error) {
	t.Helper()
	if currentDSN != "" {
		return OpenWith(Options{Driver: "postgres", DSN: currentDSN})
	}
	return Open(path)
}

// testDatabase makes a database of the test's own on the server the DSN names, dropped when the
// test ends. It mirrors internal/kv's helper (test packages cannot share code).
func testDatabase(t *testing.T, dsn string) string {
	t.Helper()
	raw := make([]byte, 4)
	_, _ = rand.Read(raw)
	name := "beacon_test_" + hex.EncodeToString(raw)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		// the store's pools close asynchronously; FORCE drops through what is still open, but a
		// connection in flight can beat it — try again before giving up
		for range 3 {
			dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err := admin.ExecContext(dctx, "DROP DATABASE "+name+" WITH (FORCE)")
			dcancel()
			if err == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && u.Host != "" {
		u.Path = "/" + name
		return u.String()
	}
	for _, k := range []string{"dbname", "database"} {
		if i := strings.Index(dsn, k+"="); i >= 0 {
			rest := dsn[i+len(k)+1:]
			if j := strings.IndexAny(rest, " 	"); j >= 0 {
				rest = rest[j:]
			} else {
				rest = ""
			}
			return dsn[:i] + k + "=" + name + rest
		}
	}
	return dsn + " dbname=" + name
}

func TestGroups(t *testing.T) {
	s, _ := open(t)
	now := time.Now()
	for i, st := range []string{"resolved", "firing", "firing"} {
		g := Group{ID: string(rune('a' + i)), Status: st, StartsAt: now.Add(time.Duration(i) * time.Minute), Ack: &Ack{By: "x"}}
		if err := s.PutGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	gs := s.Groups()
	if len(gs) != 3 || gs[0].ID != "c" || gs[1].ID != "b" || gs[2].ID != "a" {
		t.Fatalf("firing first, newest first: %v", []string{gs[0].ID, gs[1].ID, gs[2].ID})
	}
	if g, ok := s.Group("b"); !ok || g.Ack == nil || g.Ack.By != "x" || g.Updated.IsZero() {
		t.Fatalf("Group: %+v", g)
	}
	if n := s.DeleteGroups(func(g Group) bool { return !g.Firing() }); n != 1 {
		t.Fatalf("deleted %d", n)
	}
	if _, ok := s.Group("a"); ok {
		t.Fatal("resolved group should be gone")
	}
}

func TestTokens(t *testing.T) {
	s, path := open(t)
	secret, tok, err := s.CreateToken("claude-code", []string{"status:read", "alerts:write"}, 24*time.Hour, "cli:root")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, TokenPrefix) || len(secret) != 56 || tok.Hash == secret {
		t.Fatalf("secret %q, token %+v", secret, tok)
	}
	if _, _, err := s.CreateToken("claude-code", []string{"status:read"}, time.Hour, ""); err == nil {
		t.Error("duplicate names should be refused")
	}
	for _, bad := range []struct {
		name   string
		scopes []string
		ttl    time.Duration
	}{{"Bad Name", []string{"status:read"}, time.Hour}, {"x", nil, time.Hour}, {"x", []string{"root"}, time.Hour}, {"x", []string{"status:read"}, time.Minute}} {
		if _, _, err := s.CreateToken(bad.name, bad.scopes, bad.ttl, ""); err == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
	if got, ok := s.VerifyToken(secret); !ok || !got.Has("alerts:write") || got.Has("incidents:write") {
		t.Fatalf("VerifyToken: %+v %v", got, ok)
	}
	if _, ok := s.VerifyToken(secret + "x"); ok {
		t.Error("a wrong secret verified")
	}
	if _, ok := s.VerifyToken("Bearer " + secret); ok {
		t.Error("a malformed secret verified")
	}
	// survives a restart
	_ = s.Close()
	s2, err := reopen(t, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, ok := s2.VerifyToken(secret); !ok {
		t.Fatal("token lost on reopen")
	}
	s2.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if _, ok := s2.VerifyToken(secret); ok {
		t.Error("an expired token verified")
	}
	if err := s2.RevokeToken("claude-code"); err != nil {
		t.Fatal(err)
	}
	if err := s2.RevokeToken("claude-code"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking twice: %v", err)
	}
	if len(s2.Tokens()) != 0 {
		t.Error("token still listed")
	}
}

// TestLocked is the SQLite file's lock: a second process cannot hold the database. PostgreSQL is a
// server that any number may talk to — that is the premise of the primary/standby pair — so the
// test is the file backend's own.
func TestLocked(t *testing.T) {
	if os.Getenv("BEACON_TEST_POSTGRES_DSN") != "" {
		t.Skip("PostgreSQL holds no file to lock; its connections are the HA pair's premise")
	}
	_, path := open(t)
	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second open: %v", err)
	}
}

func TestOpenReadWith(t *testing.T) {
	s, path := open(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	o := Options{Path: path, Retention: 24 * time.Hour}
	if currentDSN != "" {
		o.Driver, o.DSN, o.Path = "postgres", currentDSN, ""
	}
	r, err := OpenReadWith(o)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.VerifyAudit(); err != nil {
		t.Fatal(err)
	}
}

func TestAuditAndMeta(t *testing.T) {
	s, _ := open(t)
	for i := range 3 {
		if err := s.Audit(AuditEntry{Actor: "a", Action: "ack", Target: string(rune('x' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	log := s.AuditLog(2)
	if len(log) != 2 || log[0].Target != "z" || log[0].Time.IsZero() {
		t.Fatalf("audit log: %+v", log)
	}
	if s.Add("feishu-2026-10", 3) != 3 || s.Add("feishu-2026-10", 2) != 5 || s.Counter("feishu-2026-10") != 5 {
		t.Error("counter")
	}
	when := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := s.SetTime("drill", when); err != nil {
		t.Fatal(err)
	}
	if !s.Time("drill").Equal(when) {
		t.Errorf("SetTime/Time: %v", s.Time("drill"))
	}
	_ = s.SetTime("drill", time.Time{})
	if !s.Time("drill").IsZero() {
		t.Error("zero time should delete")
	}
}

func TestSubjectTokens(t *testing.T) {
	s, _ := open(t)
	if _, _, err := s.CreateToken("probe-x", []string{"probe:write"}, time.Hour, ""); err == nil || !strings.Contains(err.Error(), "needs a subject") {
		t.Fatalf("a probe token without a subject: %v", err)
	}
	if _, _, err := s.CreateSubjectToken("probe-x", "Bad Name", []string{"probe:write"}, time.Hour, ""); err == nil {
		t.Fatal("a subject must be lower case letters, digits and dashes")
	}
	secret, tok, err := s.CreateSubjectToken("probe-cdn-pre-us1", "cdn-pre-us1", []string{"probe:write"}, time.Hour, "cli:root")
	if err != nil || tok.Subject != "cdn-pre-us1" {
		t.Fatalf("CreateSubjectToken: %+v %v", tok, err)
	}
	if got, ok := s.VerifyToken(secret); !ok || got.Subject != "cdn-pre-us1" || !got.Has("probe:write") || got.Has("status:read") {
		t.Fatalf("VerifyToken: %+v %v", got, ok)
	}
	// a heartbeat token may name its sender, or not
	if _, _, err := s.CreateToken("nas-backup", []string{"heartbeat:write"}, time.Hour, ""); err != nil {
		t.Fatal(err)
	}
}
