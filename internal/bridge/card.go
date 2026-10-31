package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// t renders a message in the bridge's language.
func (b *Bridge) t(format string, args ...any) string { return i18n.T(b.lang, format, args...) }

func (b *Bridge) severityName(sev string) string {
	switch sev {
	case "critical":
		return b.t("Critical")
	case "info":
		return b.t("Info")
	}
	return b.t("Warning")
}

// title is the alert's summary annotation; for a group of several services (path warnings are grouped
// by alert name), the first one's summary and how many more; otherwise its name and service.
func (b *Bridge) title(g *store.Group) string {
	if s := g.Annotations["summary"]; s != "" {
		return s
	}
	if svcs := b.services(g); len(svcs) > 1 {
		for _, a := range g.Alerts {
			if s := a.Annotations["summary"]; s != "" && a.Status == "firing" {
				return b.t("%s and %d more", s, len(svcs)-1)
			}
		}
	}
	name := g.Common["alertname"]
	if name == "" {
		name = g.Labels["alertname"]
	}
	if svc := b.service(g); svc != "" {
		return name + ": " + svc
	}
	return name
}

func (b *Bridge) service(g *store.Group) string {
	if s := g.Labels["service"]; s != "" {
		return s
	}
	return g.Common["service"]
}

// services lists the distinct services of the firing alerts, in order.
func (b *Bridge) services(g *store.Group) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range g.Alerts {
		s := a.Labels["service"]
		if a.Status != "firing" || s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// link fills a URL template's {service} and {alertname}.
func (b *Bridge) link(tmpl string, g *store.Group) string {
	if tmpl == "" {
		return ""
	}
	if v := g.Annotations["runbook"]; v != "" && tmpl == b.d.Config.Links.Runbook {
		return v
	}
	if v := g.Annotations["dashboard"]; v != "" && tmpl == b.d.Config.Links.GrafanaService {
		return v
	}
	name := g.Common["alertname"]
	if name == "" {
		name = g.Labels["alertname"]
	}
	return strings.NewReplacer("{service}", b.service(g), "{alertname}", name).Replace(tmpl)
}

func (b *Bridge) clock(t time.Time) string { return t.In(b.d.Config.Location).Format("01-02 15:04") }

// dur renders a duration roughly, the way people say it.
func (b *Bridge) dur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return b.t("under a minute")
	case d < time.Hour:
		return b.t("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return b.t("%d h", h)
		}
		return b.t("%d h %d min", h, m)
	}
	days, h := int(d.Hours())/24, int(d.Hours())%24
	return b.t("%d d %d h", days, h)
}

func cardHash(g *store.Group) string {
	parts := make([]string, 0, len(g.Alerts)+1)
	for _, a := range g.Alerts {
		parts = append(parts, a.Fingerprint+":"+a.Status)
	}
	sort.Strings(parts)
	ack, sil := "", ""
	if g.Ack != nil {
		ack = g.Ack.By + g.Ack.At.String()
	}
	if g.Silence != nil {
		sil = g.Silence.ID
	}
	parts = append(parts, g.Status, ack, sil, g.Buzzed.String(), g.Pushed.String())
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:8])
}

// locations returns "✅ id  ❌ id" for the service's probes over the last three minutes, or "". Cards are
// internal, so they show the location ids, which name the probe's machine; the public names (where and on
// which network) are for the status page.
func (b *Bridge) locations(ctx context.Context, g *store.Group) string {
	svc := b.service(g)
	if b.d.VM == nil || svc == "" || strings.ContainsAny(svc, `"\`) {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	rows, err := b.d.VM.Query(ctx, fmt.Sprintf(`max by (location) (max_over_time(probe_success{service="%s"}[3m]))`, svc))
	if err != nil || len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Labels["location"] < rows[j].Labels["location"] })
	var parts []string
	for _, r := range rows {
		mark := "✅"
		if r.Value < 1 {
			mark = "❌"
		}
		parts = append(parts, mark+" "+r.Labels["location"])
	}
	return strings.Join(parts, "  ")
}

// card renders the group's card.
func (b *Bridge) card(ctx context.Context, g *store.Group) feishu.Card {
	now := b.now()
	firing := g.Firing()
	colour := feishu.Orange
	switch {
	case !firing:
		colour = feishu.Green
	case g.Ack != nil:
		colour = feishu.Yellow
	case g.Severity == "critical":
		colour = feishu.Red
	case g.Severity == "info":
		colour = feishu.Blue
	}
	var subtitle string
	tags := []map[string]any{feishu.Tag(b.severityName(g.Severity), map[bool]string{true: "red", false: "orange"}[g.Severity == "critical"])}
	if firing {
		subtitle = b.t("Since %s (%s)", b.clock(g.StartsAt), b.dur(now.Sub(g.StartsAt)))
	} else {
		subtitle = b.t("Resolved at %s after %s", b.clock(g.ResolvedAt), b.dur(g.ResolvedAt.Sub(g.StartsAt)))
		tags = []map[string]any{feishu.Tag(b.t("Resolved"), "green")}
	}

	var md []string
	if d := g.Annotations["description"]; d != "" {
		md = append(md, d)
	}
	if svc := b.service(g); svc != "" {
		md = append(md, b.t("**Service**: %s", svc))
	} else if svcs := b.services(g); len(svcs) > 0 {
		const show = 10
		list := strings.Join(svcs[:min(len(svcs), show)], ", ")
		if len(svcs) > show {
			list = b.t("%s and %d more", list, len(svcs)-show)
		}
		md = append(md, b.t("**Services**: %s", list))
	}
	n := 0
	for _, a := range g.Alerts {
		if a.Status == "firing" {
			n++
		}
	}
	if firing && n > 1 {
		md = append(md, b.t("**Alerts**: %d firing", n))
	}
	if firing {
		if loc := b.locations(ctx, g); loc != "" {
			md = append(md, b.t("**Locations**: %s", loc))
		}
	}

	var state []string
	if g.Ack != nil {
		state = append(state, b.t("Acknowledged by %s at %s via %s", g.Ack.By, b.clock(g.Ack.At), b.viaName(g.Ack.Via)))
		if g.Ack.Note != "" {
			state = append(state, b.t("Note: %s", g.Ack.Note))
		}
	}
	if g.Silence != nil && now.Before(g.Silence.Until) {
		state = append(state, b.t("Silenced until %s by %s", b.clock(g.Silence.Until), g.Silence.By))
	}
	if !g.Buzzed.IsZero() {
		state = append(state, b.t("Buzzed in Feishu at %s", b.clock(g.Buzzed)))
	}
	if !g.Pushed.IsZero() {
		state = append(state, b.t("Pushover alert sent at %s", b.clock(g.Pushed)))
	}

	els := []map[string]any{feishu.Markdown(strings.Join(md, "\n"))}
	if len(state) > 0 {
		els = append(els, feishu.Markdown("<font color='grey'>"+strings.Join(state, "\n")+"</font>"))
	}
	var links []map[string]any
	if u := b.link(b.d.Config.Links.Runbook, g); u != "" {
		links = append(links, feishu.LinkButton(b.t("Runbook"), u))
	}
	if u := b.link(b.d.Config.Links.GrafanaService, g); u != "" {
		links = append(links, feishu.LinkButton(b.t("Dashboard"), u))
	}
	if firing {
		val := func(a string, extra ...string) map[string]any {
			v := map[string]any{"a": a, "g": g.ID}
			if len(extra) == 2 {
				v[extra[0]] = extra[1]
			}
			return v
		}
		var acts []map[string]any
		if g.Ack == nil {
			acts = append(acts, feishu.Button(b.t("Acknowledge"), "primary_filled", val("ack")))
		}
		if g.Silence == nil || now.After(g.Silence.Until) {
			acts = append(acts,
				feishu.Button(b.t("Silence 1h"), "default", val("silence", "d", "1h")),
				feishu.Button(b.t("Silence 4h"), "default", val("silence", "d", "4h")),
				feishu.Button(b.t("Silence 24h"), "default", val("silence", "d", "24h")))
		}
		els = append(els, feishu.Hr(), feishu.Row(append(acts, links...)...))
		if g.Ack == nil {
			els = append(els, feishu.NoteForm("ack_note", b.t("What are you doing about it?"), b.t("Acknowledge with note"), val("note")))
		}
	} else if len(links) > 0 {
		els = append(els, feishu.Hr(), feishu.Row(links...))
	}
	return feishu.NewCard(feishu.Header(b.title(g), subtitle, colour, tags...), els...)
}

// plainBody is the alert in a few lines of text (Pushover, Bark, the group bot).
func (b *Bridge) plainBody(g *store.Group) string {
	var lines []string
	if d := g.Annotations["description"]; d != "" {
		lines = append(lines, d)
	}
	lines = append(lines, b.t("Since %s (%s)", b.clock(g.StartsAt), b.dur(b.now().Sub(g.StartsAt))))
	return strings.Join(lines, "\n")
}

func (b *Bridge) plainFiring(g *store.Group) string {
	s := b.t("Firing (%s): %s", b.severityName(g.Severity), b.title(g)) + "\n" + b.plainBody(g)
	if u := b.link(b.d.Config.Links.Runbook, g); u != "" {
		s += "\n" + u
	}
	return s
}

func (b *Bridge) plainResolved(g *store.Group) string {
	return b.t("Resolved: %s, after %s", b.title(g), b.dur(g.ResolvedAt.Sub(g.StartsAt)))
}
