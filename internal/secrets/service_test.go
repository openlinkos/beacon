package secrets

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

func makeIdentity(t *testing.T, db *store.Store) (store.Principal, store.Principal) {
	t.Helper()
	code, _, err := db.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.Join(code, "alice", "Alice", "test-only-setup-password")
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := db.AccountPrincipal("alice")
	if !ok {
		t.Fatal("owner not available")
	}
	session, _, err := db.CreateAccountSession(account, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	agent := makeCredentialAgent(t, db)
	return owner.InConsoleSession(session), agent
}

func makeCredentialAgent(t *testing.T, db *store.Store) store.Principal {
	t.Helper()
	if _, err := db.CreateAgent(store.Agent{Name: "duty-agent", Owner: "alice", Preset: store.PresetCustom,
		Scopes: []string{"status:read", UseScope}, CreatedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	secret, _, err := db.CreateAgentToken("duty-agent", time.Hour, "alice")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := db.Authenticate(secret)
	if !ok || !p.Has(UseScope) {
		t.Fatal("agent token is missing the custom scope")
	}
	return p
}

func TestTeamCredentialLifecycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner, agent := makeIdentity(t, db)
	ctx := context.Background()

	const first, second = "fixture-bearer-value-31", "fixture-bearer-value-62"
	var seen atomic.Value
	var calls atomic.Int64
	var auditedAtRequest atomic.Bool
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(100)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(redirect.Close)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		seen.Store(req.Header.Get("Authorization"))
		if es, _, err := db.UsesLog().Query(events.Filter{Action: "credential.use", Limit: 1}); err == nil && len(es) == 1 {
			auditedAtRequest.Store(es[0].OnBehalf == "alice" && es[0].Actor == "alice / duty-agent")
		}
		w.Header().Set("X-Reflected-Value", req.Header.Get("Authorization"))
		w.Header().Set("Location", redirect.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = fmt.Fprint(w, req.Header.Get("Authorization"))
	}))
	t.Cleanup(upstream.Close)
	configured, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x31}, keySize)
	d := Deps{Store: db, AnchorDir: filepath.Join(dir, "vault-anchors"), AllowedHosts: []string{configured.Host}}
	s, err := Open(key, d)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the production transport's redirect policy and timeout. Trust only the fixture's CA.
	s.http.Transport = upstream.Client().Transport
	in := NewCredential{Name: "Billing health", Endpoint: upstream.URL + "/ready", Value: first}
	if _, err := s.Create(ctx, agent, "mcp", in); !errors.Is(err, ErrAccess) {
		t.Fatalf("agent could store a credential: %v", err)
	}
	if _, err := s.Create(ctx, owner, "console", NewCredential{Name: "Outside", Endpoint: redirect.URL, Value: first}); !errors.Is(err, ErrInput) {
		t.Fatalf("out-of-policy destination accepted: %v", err)
	}
	item, err := s.Create(ctx, owner, "console", in)
	if err != nil {
		t.Fatal(err)
	}
	record := func() []byte {
		var b []byte
		if err := db.Storage().View(func(tx kv.Tx) error {
			b = bytes.Clone(tx.Get(bucket, []byte(item.ID)))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if strings.Contains(string(record()), first) {
		t.Fatal("bearer token was saved unencrypted")
	}
	if list, err := s.List(agent); err != nil || len(list) != 0 {
		t.Fatalf("agent can see an ungranted credential: %v %v", list, err)
	}
	if _, err := s.Use(ctx, agent, "mcp", item.ID); !errors.Is(err, ErrAccess) || calls.Load() != 0 {
		t.Fatalf("agent used without grant: %v, calls %d", err, calls.Load())
	}
	item, err = s.SetGrant(ctx, owner, "console", item.ID, item.Version, agent.Agent, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if items, err := s.List(agent); err != nil || len(items) != 1 || items[0].Endpoint != "" || items[0].ID != item.ID {
		t.Fatalf("agent received metadata outside its grant: %v %v", items, err)
	}
	out, err := s.Use(ctx, agent, "mcp", item.ID)
	body, _ := json.Marshal(out)
	if err != nil || calls.Load() != 1 || !auditedAtRequest.Load() || seen.Load() != "Bearer "+first || string(body) != `{"status":307}` {
		t.Fatalf("fixed request, recorded use or filtered response failed: %s %v, calls %d", body, err, calls.Load())
	}
	if _, err := s.Rotate(ctx, owner, "console", item.ID, item.Version-1, second); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale form could rotate: %v", err)
	}
	item, err = s.Rotate(ctx, owner, "console", item.ID, item.Version, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Use(ctx, agent, "mcp", item.ID); err != nil || calls.Load() != 2 || seen.Load() != "Bearer "+second {
		t.Fatalf("rotated token did not take effect for existing grants: %v", err)
	}
	if err := db.DeleteAgent(agent.Agent); err != nil {
		t.Fatal(err)
	}
	agent = makeCredentialAgent(t, db)
	if _, err := s.Use(ctx, agent, "mcp", item.ID); !errors.Is(err, ErrAccess) || calls.Load() != 2 {
		t.Fatalf("agent name reuse reactivated a prior grant: %v", err)
	}
	item, err = s.SetGrant(ctx, owner, "console", item.ID, item.Version, agent.Agent, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	old := record()
	item, err = s.Revoke(ctx, owner, "console", item.ID, item.Version, agent.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Use(ctx, agent, "mcp", item.ID); !errors.Is(err, ErrAccess) || calls.Load() != 2 {
		t.Fatalf("revoked grant remained usable: %v", err)
	}
	if err := db.Storage().Update(func(tx kv.Tx) error { return tx.Put(bucket, []byte(item.ID), old) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Use(ctx, agent, "mcp", item.ID); !errors.Is(err, ErrAccess) || calls.Load() != 2 {
		t.Fatalf("a stale row reactivated a grant: %v", err)
	}
	if _, err := Open(bytes.Repeat([]byte{0x62}, keySize), d); err == nil {
		t.Fatal("a different key opened the vault")
	}
	audit, _ := json.Marshal(db.AuditLog(100))
	if bytes.Contains(audit, []byte(first)) || bytes.Contains(audit, []byte(second)) {
		t.Fatal("the audit log contains a credential value")
	}
}

func TestTeamCredentialRequiresAuditBeforeOutboundUse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner, _ := makeIdentity(t, db)
	var calls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	host, _ := url.Parse(upstream.URL)
	s, err := Open(bytes.Repeat([]byte{0x31}, keySize), Deps{Store: db, AnchorDir: filepath.Join(dir, "vault"), AllowedHosts: []string{host.Host}})
	if err != nil {
		t.Fatal(err)
	}
	s.http.Transport = upstream.Client().Transport
	ctx := context.Background()
	c, err := s.Create(ctx, owner, "console", NewCredential{Name: "Billing health", Endpoint: upstream.URL, Value: "fixture-bearer-value"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := s.Use(ctx, owner, "console", c.ID); err != nil || result.Status != http.StatusNoContent {
		t.Fatalf("owner's own use failed: %v %v", result, err)
	}
	if err := db.Storage().UpdateBoth(func(_ kv.Tx, raw *sql.Tx) error {
		_, err := raw.ExecContext(ctx, "DROP TABLE secret_uses")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Use(ctx, owner, "console", c.ID); err == nil || calls.Load() != 1 {
		t.Fatalf("request escaped without a committed audit: %v %d", err, calls.Load())
	}
	es, _, err := db.AuditQuery(store.AuditFilter{Action: "credential.use", Target: c.ID})
	if err != nil || len(es) != 1 || es[0].Actor != "alice" || es[0].Via != "console" {
		t.Fatalf("expected the audit to show only the owner's earlier, successful use: %+v %v", es, err)
	}
}

func TestTeamCredentialRevocationRequiresDurableAnchor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner, agent := makeIdentity(t, db)
	d := Deps{Store: db, AnchorDir: filepath.Join(dir, "anchor"), AllowedHosts: []string{"api.example.com"}}
	key := bytes.Repeat([]byte{0x49}, keySize)
	s, err := Open(key, d)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	item, err := s.Create(ctx, owner, "console", NewCredential{Name: "Billing", Endpoint: "https://api.example.com/ready", Value: "fixture-bearer-value"})
	if err != nil {
		t.Fatal(err)
	}
	item, err = s.SetGrant(ctx, owner, "console", item.ID, item.Version, agent.Agent, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var priorHead, priorRow []byte
	if err := db.Storage().View(func(tx kv.Tx) error {
		priorHead = bytes.Clone(tx.Get(bucket, headKey))
		priorRow = bytes.Clone(tx.Get(bucket, []byte(item.ID)))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(d.AnchorDir, "epochs.tmp")
	if err := os.Mkdir(block, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(ctx, owner, "console", item.ID, item.Version, agent.Agent); !errors.Is(err, ErrAnchorPending) {
		t.Fatalf("revocation was acknowledged without a durable anchor: %v", err)
	}
	if _, err := s.Use(ctx, agent, "mcp", item.ID); !errors.Is(err, ErrAnchorPending) {
		t.Fatalf("unanchored policy permitted an attempt: %v", err)
	}
	if _, err := Open(key, d); !errors.Is(err, ErrState) {
		t.Fatalf("restart succeeded with an epoch that could not catch up: %v", err)
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(owner)
	if err != nil || len(list) != 1 || list[0].Version != item.Version+1 || len(list[0].Grants) != 0 {
		t.Fatalf("repair did not reveal the committed revocation: %+v %v", list, err)
	}
	p := filepath.Join(d.AnchorDir, "epochs")
	anchor, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(key, d); !errors.Is(err, ErrState) {
		t.Fatalf("restart succeeded with a missing anchor: %v", err)
	}
	if err := os.WriteFile(p, anchor, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(key, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Use(ctx, agent, "mcp", item.ID); !errors.Is(err, ErrAccess) {
		t.Fatalf("restoring the known anchor exposed a revoked grant: %v", err)
	}
	if err := db.Storage().Update(func(tx kv.Tx) error {
		if err := tx.Put(bucket, headKey, priorHead); err != nil {
			return err
		}
		return tx.Put(bucket, []byte(item.ID), priorRow)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(key, d); !errors.Is(err, ErrState) {
		t.Fatalf("restoring earlier database rows against the latest independent anchor reopened the store: %v", err)
	}
}

func TestTeamCredentialDoesNotTrustPreviouslyResolvedAuthority(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner, agent := makeIdentity(t, db)
	s, err := Open(bytes.Repeat([]byte{0x59}, keySize), Deps{Store: db, AnchorDir: filepath.Join(dir, "vault"),
		AllowedHosts: []string{"api.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, err := s.Create(ctx, owner, "console", NewCredential{Name: "Billing", Endpoint: "https://api.example.com/ready", Value: "fixture-bearer-value"})
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.SetGrant(ctx, owner, "console", c.ID, c.Version, agent.Agent, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeToken(agent.Token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateAgentToken(agent.Agent, time.Hour, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Use(ctx, agent, "mcp", c.ID); !errors.Is(err, ErrAccess) {
		t.Fatalf("the replaced token's previously resolved principal could use a credential: %v", err)
	}
	limited, err := db.CreateGrant(store.Grant{Agent: agent.Agent, Client: "limited-client", Preset: "operator",
		Expires: time.Now().Add(time.Hour)}, false)
	if err != nil {
		t.Fatal(err)
	}
	oauth, ok := db.Authenticate(limited.Access)
	if !ok || oauth.Has(UseScope) {
		t.Fatal("ordinary OAuth consent inherited a custom scope")
	}
	if _, err := s.List(oauth); !errors.Is(err, ErrAccess) {
		t.Fatalf("scope-limited OAuth client discovered a credential: %v", err)
	}
	legacy, err := db.CreateGrant(store.Grant{Agent: agent.Agent, Client: "legacy-client", Expires: time.Now().Add(time.Hour)}, false)
	if err != nil {
		t.Fatal(err)
	}
	oauth, ok = db.Authenticate(legacy.Access)
	if !ok || !oauth.Has(UseScope) {
		t.Fatal("test requires an uncapped historical OAuth grant")
	}
	if items, err := s.List(oauth); err != nil || len(items) != 1 {
		t.Fatalf("authorised OAuth credential could not be resolved: %v", err)
	}
	if _, ok := db.RevokeOAuthToken(legacy.Access, "legacy-client"); !ok {
		t.Fatal("OAuth credential was not revoked")
	}
	if _, err := s.List(oauth); !errors.Is(err, ErrAccess) {
		t.Fatalf("the previously resolved OAuth credential survived revocation: %v", err)
	}
	db.DeleteAccountSessions(owner.User, "")
	if _, err := s.Rotate(ctx, owner, "console", c.ID, c.Version, "unused-replacement-value"); !errors.Is(err, ErrAccess) {
		t.Fatalf("the expired session's previously resolved principal could replace a credential: %v", err)
	}
}
