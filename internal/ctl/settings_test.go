package ctl

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/store"
)

// The socket's settings routes, as `beacon settings …` uses them: get, set, reset, history and
// revert, through the client the command line dials with.
func TestSettings(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc, err := settings.Open(settings.Deps{Store: st, Lang: i18n.EN})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(SettingsHandler(svc))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()

	list, err := c.ListSettings(ctx)
	if err != nil || len(list.Settings) < 60 || len(list.Overrides) != 0 {
		t.Fatalf("list: %d settings, %d overrides, %v", len(list.Settings), len(list.Overrides), err)
	}
	res, err := c.GetSetting(ctx, "request.max_uses")
	if err != nil || res.From != "default" {
		t.Fatalf("get: %+v %v", res, err)
	}
	ch, err := c.SetSetting(ctx, SetSettingRequest{Key: "request.max_uses", Value: json.RawMessage("50"), Reason: "tighter"})
	if err != nil || ch.Version != 1 || ch.Via != "cli" || !strings.HasPrefix(ch.Actor, "cli:") || ch.Action != "set" {
		t.Fatalf("set: %+v %v", ch, err)
	}
	if res, _ := c.GetSetting(ctx, "request.max_uses"); res.From != "system" || string(res.Value) != "50" {
		t.Fatalf("after set: %+v", res)
	}
	if _, err := c.SetSetting(ctx, SetSettingRequest{Key: "request.max_uses", Value: json.RawMessage(`"many"`)}); err == nil || !strings.Contains(err.Error(), "unlimited") {
		t.Errorf("a value that does not fit says why: %v", err)
	}
	if _, err := c.SetSetting(ctx, SetSettingRequest{Key: "no.such.key", Value: json.RawMessage("1")}); err == nil || !strings.Contains(err.Error(), "no such setting") == false {
		t.Errorf("an unknown key says so: %v", err)
	}
	h, err := c.SettingsHistory(ctx, 10)
	if err != nil || len(h) != 1 || h[0].Key != "request.max_uses" {
		t.Fatalf("history: %+v %v", h, err)
	}
	rst, err := c.ResetSetting(ctx, ResetSettingRequest{Key: "request.max_uses", Reason: "done"})
	if err != nil || rst.Action != "reset" {
		t.Fatalf("reset: %+v %v", rst, err)
	}
	if res, _ := c.GetSetting(ctx, "request.max_uses"); res.From != "default" {
		t.Fatalf("after reset: %+v", res)
	}
	back, err := c.RevertSetting(ctx, RevertSettingRequest{Version: 2}) // the reset: its value comes back
	if err != nil || back.Reverts != 2 || string(back.After) != "50" {
		t.Fatalf("revert: %+v %v", back, err)
	}
	if res, _ := c.GetSetting(ctx, "request.max_uses"); res.From != "system" || string(res.Value) != "50" {
		t.Fatalf("after revert: %+v", res)
	}
	if _, err := c.RevertSetting(ctx, RevertSettingRequest{Version: 99}); err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Errorf("an unknown version says so: %v", err)
	}
}
