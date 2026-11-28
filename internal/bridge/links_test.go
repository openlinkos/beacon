package bridge

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/router"
	"github.com/openlinkos/beacon/internal/store"
)

// linked is a firing group whose alerts name their own runbook and dashboard, from sender ("" for the
// rule engine).
func linked(sender string) alertmanager.Webhook {
	w := hook("firing", "critical", "f1")
	w.CommonAnnotations["runbook"] = "https://runbooks.example.net/fix-it-now"
	w.CommonAnnotations["dashboard"] = "https://dash.example.net/d/x"
	if sender != "" {
		w.GroupKey = `{}:{alertname="ServiceDown",sender="` + sender + `",service="git-example-com"}`
		w.GroupLabels[router.SenderLabel] = sender
		w.CommonLabels[router.SenderLabel] = sender
	}
	for i := range w.Alerts {
		w.Alerts[i].Labels = w.CommonLabels
		w.Alerts[i].Annotations = w.CommonAnnotations
	}
	return w
}

// TestLinksFromTheRuleEngineOnly: the runbook and dashboard an alert names replace the configured links
// only when the rule engine sent it; another sender's are ignored, on the card, in the chat, in the API's
// view, the evidence and the hook's payload. Either way the links are listed as untrusted text.
func TestLinksFromTheRuleEngineOnly(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		sender, runbook, dashboard string
	}{
		{"", "https://runbooks.example.net/fix-it-now", "https://dash.example.net/d/x"},
		{"flux", "https://ops.example/runbooks/ServiceDown.md", "https://grafana.example/d/svc?var-service=git-example-com"},
	} {
		e := newEnv(t)
		w := linked(tc.sender)
		if err := e.b.HandleWebhook(ctx, w); err != nil {
			t.Fatal(err)
		}
		g, ok := e.st.Group(GroupID(w.GroupKey))
		if !ok {
			t.Fatalf("%q: no group", tc.sender)
		}
		card := cardText(e.b.card(ctx, &g))
		if !strings.Contains(card, tc.runbook) || !strings.Contains(card, tc.dashboard) || (tc.sender != "" && strings.Contains(card, "example.net")) {
			t.Errorf("%q: card %s", tc.sender, card)
		}
		if text := e.b.plainFiring(&g); !strings.Contains(text, tc.runbook) || (tc.sender != "" && strings.Contains(text, "example.net")) {
			t.Errorf("%q: chat %s", tc.sender, text)
		}
		if v := e.b.view(&g); v.Runbook != tc.runbook || v.Dashboard != tc.dashboard {
			t.Errorf("%q: view %s %s", tc.sender, v.Runbook, v.Dashboard)
		}
		ev := e.b.evidence(ctx, &g, e.b.window(&g, defaultBefore), nil)
		if ev.Links.Runbook != tc.runbook || ev.Links.Dashboard != tc.dashboard {
			t.Errorf("%q: evidence links %+v", tc.sender, ev.Links)
		}
		var p HookPayload
		if err := json.Unmarshal(e.b.hookBody(&g, config.Agent{Name: "claude-oncall", Kind: "webhook"}, store.AgentCall{Event: "firing"}), &p); err != nil {
			t.Fatal(err)
		}
		if p.Links.Runbook != tc.runbook || p.Links.Dashboard != tc.dashboard {
			t.Errorf("%q: hook links %+v", tc.sender, p.Links)
		}
		for _, fields := range [][]string{ev.Untrusted.Fields, p.Untrusted.Fields} {
			if !slices.Contains(fields, "links.runbook") || !slices.Contains(fields, "links.dashboard") {
				t.Errorf("%q: links are not listed as untrusted: %v", tc.sender, fields)
			}
		}
	}
}
