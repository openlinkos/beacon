package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/scrub"
)

// The use paths: bound machines and hosts, a local process, attachments, the policy head after a
// use (Used sits in the grant's signed parts), record-before-give, and file re-delivery.

type catchDeliver struct {
	mu  sync.Mutex
	got []deliveredFile
}

type deliveredFile struct {
	machine, path, content, owner, mode string
}

func (c *catchDeliver) DeliverFile(_ context.Context, machine, path, content, owner, mode string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, deliveredFile{machine, path, content, owner, mode})
	return nil
}

func (c *catchDeliver) last() deliveredFile {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.got) == 0 {
		return deliveredFile{}
	}
	return c.got[len(c.got)-1]
}

func (c *catchDeliver) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

// useReady is an auto-level Infra collection, an on-call entitlement that includes use, a worker
// agent, one item with the fixture password, and a live use grant. machines, hosts and files are
// optional bindings.
func (g *grantsVault) useReady(t *testing.T, machines, hosts []string, files []ItemFile) (Collection, Item, string) {
	t.Helper()
	infra, err := g.CreateCollection(g.owner, "Infra", "", LevelAuto, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt(ActDescribe, ActRead, ActUse))
	g.newAgent(t, "worker", "own")
	item, err := g.SetItem(g.owner, ItemIn{Collection: infra.Name, Title: "apitoken", Level: LevelAuto,
		Machines: machines, Hosts: hosts, Files: files,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Purpose: PurposePassword, Value: fixtureToken}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{item.Title}, Action: ActUse, Reason: "fix the dns outage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Granted) != 1 {
		t.Fatalf("want one grant, got %+v", out)
	}
	return infra, item, RefOf(infra.Name, item.Title, "", "password")
}

func TestUseForMachineBound(t *testing.T) {
	g := newGrantsVault(t)
	_, _, ref := g.useReady(t, []string{"edge-1"}, nil, nil)
	vals, sc, err := g.UseForMachine("worker", "edge-1", []UseAsk{{Name: "TOKEN", Ref: ref}}, "fix the dns outage", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if vals["TOKEN"] != fixtureToken {
		t.Fatalf("got %q", vals["TOKEN"])
	}
	hidden := sc.Scrub("the token is " + fixtureToken)
	if strings.Contains(hidden, fixtureToken) || !strings.Contains(hidden, "[secret:") {
		t.Fatalf("the scrubber did not hide the value: %q", hidden)
	}
	got := g.GrantsOf("worker")
	if len(got) != 1 {
		t.Fatalf("grant vanished after use: %+v", got)
	}
	if got[0].Used != 1 {
		t.Fatalf("used %d", got[0].Used)
	}
}

func TestUseForMachineUnbound(t *testing.T) {
	g := newGrantsVault(t)
	_, _, ref := g.useReady(t, []string{"edge-1"}, nil, nil)
	_, _, err := g.UseForMachine("worker", "edge-2", []UseAsk{{Name: "TOKEN", Ref: ref}}, "fix the dns outage", "", "test")
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("unbound machine: %v", err)
	}
}

func TestUseForProcess(t *testing.T) {
	g := newGrantsVault(t)
	_, _, ref := g.useReady(t, nil, nil, nil)
	vals, err := g.UseForProcess("worker", "cli", map[string]string{"TOKEN": ref}, "fix the dns outage", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if vals["TOKEN"] != fixtureToken {
		t.Fatalf("got %q", vals["TOKEN"])
	}
	got := g.GrantsOf("worker")
	if len(got) != 1 || got[0].Used != 1 {
		t.Fatalf("grant after process use: %+v", got)
	}
}

func TestUseNever(t *testing.T) {
	g := newGrantsVault(t)
	infra, err := g.CreateCollection(g.owner, "Infra", "", LevelAuto, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt(ActDescribe, ActRead, ActUse))
	g.newAgent(t, "worker", "own")
	item, err := g.SetItem(g.owner, ItemIn{Collection: infra.Name, Title: "neveritem", Level: LevelNever,
		Machines: []string{"edge-1"},
		Fields:   []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}}})
	if err != nil {
		t.Fatal(err)
	}
	ref := RefOf(infra.Name, item.Title, "", "password")
	_, _, err = g.UseForMachine("worker", "edge-1", []UseAsk{{Name: "TOKEN", Ref: ref}}, "fix the dns outage", "", "test")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("never: %v", err)
	}
}

func TestUseAttachment(t *testing.T) {
	g := newGrantsVault(t)
	infra, err := g.CreateCollection(g.owner, "Infra", "", LevelAuto, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.ent(t, oncallEnt(ActDescribe, ActRead, ActUse))
	g.newAgent(t, "worker", "own")
	item, err := g.SetItem(g.owner, ItemIn{Collection: infra.Name, Title: "sshkey", Level: LevelAuto,
		Fields:      []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureToken}},
		Attachments: []AttachmentIn{{Name: "id_rsa", Data: []byte(fixtureAttach)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.RequestSecret("worker", "test", RequestIn{Items: []string{item.Title}, Action: ActUse, Reason: "fix the dns outage"}); err != nil {
		t.Fatal(err)
	}
	ref := RefOf(infra.Name, item.Title, "", "id_rsa")
	vals, err := g.UseForProcess("worker", "cli", map[string]string{"KEY": ref}, "fix the dns outage", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if vals["KEY"] != fixtureAttach {
		t.Fatalf("attachment mismatch")
	}
}

func TestHTTPRequestUnbound(t *testing.T) {
	g := newGrantsVault(t)
	_, _, ref := g.useReady(t, nil, []string{"api.example.com"}, nil)
	_, err := g.HTTPRequest("worker", "test", HTTPRequestIn{
		URL:     "https://other.example.com/x",
		Headers: map[string]string{"Authorization": "Bearer {{secret." + ref + "}}"},
		Reason:  "fix the dns outage",
	}, "", "test")
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("unbound host: %v", err)
	}
}

func TestHTTPRequestBoundAndScrubbed(t *testing.T) {
	g := newGrantsVault(t)
	g.setSetting(t, "http.allow_private", true)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "got %s", r.Header.Get("Authorization"))
	}))
	t.Cleanup(srv.Close)
	g.d.HTTP = srv.Client()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _, ref := g.useReady(t, nil, []string{u.Hostname()}, nil)
	out, err := g.HTTPRequest("worker", "test", HTTPRequestIn{
		Method:  "GET",
		URL:     srv.URL + "/x",
		Headers: map[string]string{"Authorization": "Bearer {{secret." + ref + "}}"},
		Reason:  "fix the dns outage",
	}, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Body, fixtureToken) {
		t.Fatalf("the answer leaked the value: %q", out.Body)
	}
	if !strings.Contains(out.Body, scrub.Placeholder(ref)) && !strings.Contains(out.Body, "[secret:") {
		t.Fatalf("the answer was not scrubbed: %q", out.Body)
	}
}

func TestHTTPRequestPrivate(t *testing.T) {
	g := newGrantsVault(t)
	_, _, ref := g.useReady(t, nil, []string{"127.0.0.1"}, nil)
	_, err := g.HTTPRequest("worker", "test", HTTPRequestIn{
		URL:     "https://127.0.0.1:9/x",
		Headers: map[string]string{"Authorization": "Bearer {{secret." + ref + "}}"},
		Reason:  "fix the dns outage",
	}, "", "test")
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("private address: %v", err)
	}
	got := g.GrantsOf("worker")
	if len(got) != 1 || got[0].Used != 0 {
		t.Fatalf("a refused private request must not consume the grant: %+v", got)
	}
}

func TestFileRedeliverOnChange(t *testing.T) {
	g := newGrantsVault(t)
	catch := &catchDeliver{}
	g.SetUseDeps(UseDeps{Deliver: catch})
	infra, item, _ := g.useReady(t, nil, nil, []ItemFile{{Machine: "edge-1", Path: "/etc/app/token", Field: "password", Mode: "0600"}})
	if catch.n() != 1 {
		t.Fatalf("first delivery count %d", catch.n())
	}
	if got := catch.last(); got.content != fixtureToken || got.path != "/etc/app/token" || got.mode != "0600" {
		t.Fatalf("first delivery %+v", got)
	}
	const next = "fixture-value-rotated-thud-wombat-31"
	if _, err := g.SetItem(g.owner, ItemIn{ID: item.ID, Collection: infra.Name,
		Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: next}}}); err != nil {
		t.Fatal(err)
	}
	if catch.n() != 2 {
		t.Fatalf("second delivery count %d", catch.n())
	}
	if got := catch.last(); got.content != next {
		t.Fatalf("second delivery %+v", got)
	}
}

func TestUseRecordBeforeGive(t *testing.T) {
	g := newGrantsVault(t)
	_, _, ref := g.useReady(t, []string{"edge-1"}, nil, nil)
	g.d.AppendUse = func(*sql.Tx, *events.Event) error { return errors.New("the log is shut") }
	_, _, err := g.UseForMachine("worker", "edge-1", []UseAsk{{Name: "TOKEN", Ref: ref}}, "fix the dns outage", "", "test")
	if err == nil {
		t.Fatal("the use should fail when the record cannot be written")
	}
	got := g.GrantsOf("worker")
	if len(got) != 1 || got[0].Used != 0 {
		t.Fatalf("the grant moved: %+v", got)
	}
}

func TestHostBinds(t *testing.T) {
	must := func(s string) *url.URL {
		t.Helper()
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	if !hostBinds([]string{"api.example.com"}, must("https://api.example.com/x")) {
		t.Fatal("host should bind")
	}
	if hostBinds([]string{"api.example.com"}, must("https://other.example.com/x")) {
		t.Fatal("other host should not bind")
	}
	if !hostBinds([]string{"api.example.com/v1"}, must("https://api.example.com/v1/x")) {
		t.Fatal("prefix should bind")
	}
	if hostBinds([]string{"api.example.com/v1"}, must("https://api.example.com/v2/x")) {
		t.Fatal("other prefix should not bind")
	}
	if !hostBinds([]string{"api.example.com:8443"}, must("https://api.example.com:8443/x")) {
		t.Fatal("port should bind")
	}
	if hostBinds([]string{"api.example.com:8443"}, must("https://api.example.com/x")) {
		t.Fatal("missing port should not bind")
	}
}
