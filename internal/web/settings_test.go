package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/settings"
)

// The console's settings page: every setting with the value in force, the default and where the
// value was overridden; a change and a reset for whoever holds secrets:admin, the history with
// revert; a reader sees the page without the forms, and rows that failed their signature are said.
func TestSettingsPage(t *testing.T) {
	e := newEnv(t, i18n.EN)
	svc, err := settings.Open(settings.Deps{Store: e.st, Bot: e.bot, Lang: i18n.EN, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.Settings = svc

	b := e.browser()
	if a := b.signIn(e.token("reader", "status:read"), "/console/settings"); a.code != http.StatusSeeOther {
		t.Fatalf("a reader signs in: %d", a.code)
	}
	a := b.get("/console/settings")
	if a.code != http.StatusOK || !strings.Contains(a.body, "request.max_uses") || !strings.Contains(a.body, "unlimited") {
		t.Fatalf("the settings are listed: %d", a.code)
	}
	if strings.Contains(a.body, "/settings/request.max_uses/set") {
		t.Error("a reader gets no change form")
	}

	// an owner changes one, resets it, and the history with revert is there
	admin := e.browser()
	if a := admin.signIn(e.token("owner", "status:read", "secrets:admin", "config:write", "accounts:write"), "/console/settings"); a.code != http.StatusSeeOther {
		t.Fatalf("an owner signs in: %d", a.code)
	}
	a = admin.get("/console/settings")
	if !strings.Contains(a.body, "/settings/request.max_uses/set") {
		t.Fatal("an owner gets the change form")
	}
	form := url.Values{"value": {"50"}, "reason": {"tighter while testing"}}
	if a := admin.post("/console/settings/request.max_uses/set", form); a.code != http.StatusSeeOther {
		t.Fatalf("change: %d %s", a.code, a.body)
	}
	a = admin.get("/console/settings")
	if !strings.Contains(a.body, ">50<") && !strings.Contains(a.body, "50") {
		t.Fatalf("the new value shows: %s", a.body)
	}
	if !strings.Contains(a.body, "tighter while testing") {
		t.Fatalf("the reason shows: %s", a.body)
	}
	// a value that does not fit is refused, in the page's words
	a = admin.post("/console/settings/request.max_uses/set", url.Values{"value": {`"many"`}, "reason": {"x"}})
	if a.code != http.StatusSeeOther {
		t.Fatalf("bad value: %d", a.code)
	}
	if back := a.header.Get("Location"); !strings.Contains(back, "error=setting_invalid") {
		t.Fatalf("the form is refused with its problem's code: %s", back)
	}
	if a := admin.get(a.header.Get("Location")); !strings.Contains(a.body, "The value does not fit this setting") {
		t.Fatalf("the problem shows: %d", a.code)
	}
	// the history and the revert
	if a := admin.post("/console/settings/history/1/revert", url.Values{"reason": {""}}); a.code != http.StatusSeeOther {
		t.Fatalf("revert: %d %s", a.code, a.body)
	}
	if res, err := svc.System("request.max_uses"); err != nil || res.From != "default" {
		t.Fatalf("after revert: %+v %v", res, err)
	}

	// an override at another level is listed, and a row that failed its signature is said
	if _, err := svc.Set(context.Background(), settings.Actor{Name: "alice", Via: "api"}, settings.ScopeTag, "env/prod",
		"grant.alert_grace", json.RawMessage(`"30m"`), "", 0); err != nil {
		t.Fatal(err)
	}
	a = admin.get("/console/settings")
	if !strings.Contains(a.body, "env/prod") || !strings.Contains(a.body, "grant.alert_grace") {
		t.Fatalf("the override shows: %s", a.body)
	}
	if strings.Count(a.body, "tag") < 1 {
		t.Fatal("the override's scope shows")
	}

	// the Chinese page holds the same
	zhEnv := newEnv(t, i18n.ZH)
	zhSvc, err := settings.Open(settings.Deps{Store: zhEnv.st, Bot: zhEnv.bot, Lang: i18n.ZH, Now: func() time.Time { return zhEnv.now }})
	if err != nil {
		t.Fatal(err)
	}
	zhEnv.srv.Settings = zhSvc
	zh := zhEnv.browser()
	if a := zh.signIn(zhEnv.token("reader", "status:read"), "/console/settings"); a.code != http.StatusSeeOther {
		t.Fatalf("the Chinese sign-in: %d", a.code)
	}
	if a := zh.get("/console/settings"); a.code != http.StatusOK || !strings.Contains(a.body, i18n.T(i18n.ZH, "System settings")) || !strings.Contains(a.body, "request.max_uses") {
		t.Fatalf("the Chinese page: %d", a.code)
	}
}

// A page without the settings service (a Beacon built before them) renders empty, not broken.
func TestSettingsPageWithoutService(t *testing.T) {
	e := newEnv(t, i18n.EN)
	b := e.browser()
	if a := b.signIn(e.token("reader", "status:read"), "/console/settings"); a.code != http.StatusSeeOther {
		t.Fatalf("sign in: %d", a.code)
	}
	if a := b.get("/console/settings"); a.code != http.StatusOK {
		t.Fatalf("the empty page: %d", a.code)
	}
}
