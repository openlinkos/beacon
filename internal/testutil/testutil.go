// Package testutil builds a bridge with fake collaborators, for the tests of the packages above it.
package testutil

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openlinkos/status/internal/alertmanager"
	"github.com/openlinkos/status/internal/bridge"
	"github.com/openlinkos/status/internal/config"
	"github.com/openlinkos/status/internal/store"
)

// AM is a fake Alertmanager.
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
	e.Bridge = bridge.New(bridge.Deps{Config: cfg, Store: st, AM: e.AM, Bot: e.Bot})
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
