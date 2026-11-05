package main

// The vault's grants wiring: the secret events' delivery (the agent hooks configured under the
// agents' names, signed the way the on-call hooks are), the approval cards over the Feishu app,
// the alert state a bound grant outlives by its grace, the review-agent check, the sweep the
// clock runs, and the metrics. One file, so main.go keeps one line for each.

import (
	"context"
	"log/slog"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
)

// secretHookSender delivers the secret events to the webhook the configuration names for the
// agent (agents.<name>, kind webhook). An agent with no hook configured hears of nothing.
type secretHookSender struct {
	hooks []config.Agent
}

func (s *secretHookSender) SendSecretHook(_ context.Context, agent, event string, payload []byte) error {
	for _, h := range s.hooks {
		if h.Name != agent || h.Kind != "webhook" {
			continue
		}
		hook := &secrets.HookHTTP{URL: h.URL, Secret: h.Secret, Headers: h.Headers}
		return hook.SendSecretHook(context.Background(), agent, event, payload)
	}
	return nil // no hook for this agent: nothing to deliver
}

// alertStateOf tells whether the alert group a grant is bound to is firing, and when it resolved.
func alertStateOf(st *store.Store) func(string) (bool, time.Time, bool) {
	return func(id string) (bool, time.Time, bool) {
		g, ok := st.Group(id)
		if !ok {
			return false, time.Time{}, false
		}
		return g.Firing(), g.ResolvedAt, true
	}
}

// reviewAgentOf says whether the agent may decide requests as a review agent: the secrets:review
// scope, which its preset or its own scopes give, within its owner's role as always.
func reviewAgentOf(st *store.Store) func(string) bool {
	return func(agent string) bool {
		p, ok := st.Principal("agent:" + agent)
		return ok && p.Has("secrets:review")
	}
}

// gateDeliver adapts the SSH channel's DeliverFile (which answers with a session id) to the
// vault's FileDeliverer (which wants only an error).
type gateDeliver struct{ g *sshgate.Gate }

func (d gateDeliver) DeliverFile(ctx context.Context, machine, path, content, owner, mode string) error {
	_, err := d.g.DeliverFile(ctx, machine, path, content, owner, mode)
	return err
}

// grantsDepsOf gathers the vault's grants dependencies the configuration and the store give.
func grantsDepsOf(cfg *config.Config, st *store.Store, app feishu.API) secrets.GrantsDeps {
	d := secrets.GrantsDeps{
		Hooks:       &secretHookSender{hooks: cfg.Agents},
		AlertState:  alertStateOf(st),
		ReviewAgent: reviewAgentOf(st),
	}
	if app != nil && cfg.Feishu.ChatID != "" {
		d.Cards = &secrets.CardOverFeishu{API: app, ChatID: cfg.Feishu.ChatID}
	}
	return d
}

// registerSecretMetrics adds the grants' metrics: the uses and the requests as counters, the
// grants as gauges.
func registerSecretMetrics(m *metrics.Metrics, v *secrets.Service) {
	m.Register("beacon_secret_uses_total", "Secret uses by collection, agent and action", "counter",
		func() []metrics.Sample { return v.MetricsSamples() })
	m.Register("beacon_secret_requests_total", "Secret requests by decision and the decider's kind", "counter",
		func() []metrics.Sample { return v.RequestSamples() })
	m.Register("beacon_secret_grants_active", "Secret grants live now", "gauge",
		func() []metrics.Sample {
			active, _ := v.GrantGauge()
			return []metrics.Sample{{Value: float64(active)}}
		})
	m.Register("beacon_secret_grant_expiring", "Secret grants within their expiry notice", "gauge",
		func() []metrics.Sample {
			_, expiring := v.GrantGauge()
			return []metrics.Sample{{Value: float64(expiring)}}
		})
}

// sweepSecrets runs the clock's work over the grants: endings, notices, the pending TTLs.
func sweepSecrets(v *secrets.Service, log *slog.Logger) {
	if n := v.Sweep(); n > 0 {
		log.Info("the secrets' sweep changed things", "changed", n)
	}
}
