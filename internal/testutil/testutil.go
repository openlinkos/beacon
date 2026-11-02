// Package testutil builds a bridge with fake collaborators, for the tests of the packages above it.
package testutil

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/targets"
)

// AM is a fake silencer, standing in for the router.
type AM struct {
	mu      sync.Mutex
	Created []map[string]string
	Expired []string
}

func (a *AM) CreateSilence(_ context.Context, l map[string]string, _, _ time.Time, _, _ string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Created = append(a.Created, l)
	return fmt.Sprintf("sil-%d", len(a.Created)), nil
}

func (a *AM) ExpireSilence(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Expired = append(a.Expired, id)
	return nil
}

func (a *AM) Silences(context.Context) ([]alertmanager.Silence, error) {
	return []alertmanager.Silence{{ID: "sil-1", Comment: "deploy"}}, nil
}

// Bot is a fake group bot.
type Bot struct {
	mu    sync.Mutex
	Texts []string
}

func (b *Bot) Send(_ context.Context, t string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Texts = append(b.Texts, t)
	return nil
}

// Env is a bridge on a temporary store, with no Feishu app (alerts go to the fake bot).
type Env struct {
	Bridge *bridge.Bridge
	Store  *store.Store
	AM     *AM
	Bot    *Bot
}

// New creates an Env with one critical alert group firing; its id is GroupID.
func New(t *testing.T) *Env {
	t.Helper()
	return NewWith(t, nil)
}

// NewWith is New with a last word on the bridge's dependencies, for tests that add a collaborator.
func NewWith(t *testing.T, tweak func(*bridge.Deps)) *Env {
	t.Helper()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	cfg := &config.Config{
		Language: "en", Location: loc, AckTTL: config.Duration(4 * time.Hour),
		Feishu:     config.Feishu{MonthlyBudget: 1000},
		OnCallByID: map[string]config.OnCaller{},
		Escalation: map[string]config.Policy{"critical": {Channel: "webhook"}, "warning": {Channel: "webhook"}, "info": {Channel: "webhook"}},
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &Env{Store: st, AM: &AM{}, Bot: &Bot{}}
	deps := bridge.Deps{Config: cfg, Store: st, AM: e.AM, Bot: e.Bot}
	if tweak != nil {
		tweak(&deps)
	}
	e.Bridge = bridge.New(deps)
	err = e.Bridge.HandleWebhook(context.Background(), alertmanager.Webhook{
		Version: "4", GroupKey: GroupKey, Status: "firing",
		GroupLabels:       map[string]string{"alertname": "ServiceDown", "service": "hub-linkos-io"},
		CommonLabels:      map[string]string{"alertname": "ServiceDown", "service": "hub-linkos-io", "severity": "critical"},
		CommonAnnotations: map[string]string{"summary": "hub.linkos.io is down"},
		Alerts:            []alertmanager.Alert{{Status: "firing", Fingerprint: "f", StartsAt: time.Now()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// GroupKey is the key of the group New fires; GroupID is its id.
const GroupKey = `{}:{alertname="ServiceDown",service="hub-linkos-io"}`

// GroupID returns the id of the group New fires.
func GroupID() string { return bridge.GroupID(GroupKey) }

// Token creates a token with scopes and returns its secret.
func (e *Env) Token(t *testing.T, name string, scopes ...string) string {
	t.Helper()
	secret, _, err := e.Store.CreateToken(name, scopes, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// ProbeBoard is a board of requests to probe on demand over two locations, edge-1 (overseas) and
// edge-2 (mainland), and these target sets:
//
//	all-web.yaml       https://hub.example.com/ (service hub-linkos-io, the alert's; module http_head)
//	all-tls.yaml       https://hub.example.com/ again, module tls_expiry
//	all-docs.yaml      https://docs.example.com/ and https://www.example.com/ (service docs)
//	overseas-api.yaml  https://api.example.com/ (service api): edge-1 alone probes it
func ProbeBoard(t *testing.T) *probenow.Board {
	t.Helper()
	cat := &catalog.Catalog{Locations: []catalog.Location{
		{ID: "edge-1", Name: "United States - Los Angeles", Region: "overseas"},
		{ID: "edge-2", Name: "Mainland China - Shanghai", Region: "mainland"},
	}}
	set, err := targets.Build(map[string][]targets.Group{
		"all-web.yaml":      {{Targets: []string{"https://hub.example.com/"}, Labels: map[string]string{"module": "http_head", "service": "hub-linkos-io"}}},
		"all-tls.yaml":      {{Targets: []string{"https://hub.example.com/"}, Labels: map[string]string{"module": "tls_expiry", "service": "hub-linkos-io"}}},
		"all-docs.yaml":     {{Targets: []string{"https://docs.example.com/", "https://www.example.com/"}, Labels: map[string]string{"module": "http_head", "service": "docs"}}},
		"overseas-api.yaml": {{Targets: []string{"https://api.example.com/"}, Labels: map[string]string{"module": "http_2xx", "service": "api"}}},
	}, []string{"edge-1", "edge-2"})
	if err != nil {
		t.Fatal(err)
	}
	return probenow.New(probenow.Deps{Catalog: func() *catalog.Catalog { return cat }, Targets: func() *targets.Set { return set }})
}
