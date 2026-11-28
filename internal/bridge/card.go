package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/router"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
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

// link fills a URL template's {service} and {alertname}. The rule engine's runbook and dashboard
// annotations replace the configured runbook and dashboard; another sender's do not.
func (b *Bridge) link(tmpl string, g *store.Group) string {
	if tmpl == "" {
		return ""
	}
	if v := g.Annotations["runbook"]; v != "" && tmpl == b.d.Config.Links.Runbook && fromRuleEngine(g) {
		return v
	}
	if v := g.Annotations["dashboard"]; v != "" && tmpl == b.d.Config.Links.GrafanaService && fromRuleEngine(g) {
		return v
	}
	name := g.Common["alertname"]
	if name == "" {
		name = g.Labels["alertname"]
	}
	return strings.NewReplacer("{service}", b.service(g), "{alertname}", name).Replace(tmpl)
}

// fromRuleEngine reports whether g holds only the rule engine's alerts: none of them, nor the group,
// names a sender (the router labels every other sender's alerts with it).
func fromRuleEngine(g *store.Group) bool {
	if g.Labels[router.SenderLabel] != "" || g.Common[router.SenderLabel] != "" {
		return false
	}
	for _, a := range g.Alerts {
		if a.Labels[router.SenderLabel] != "" {
			return false
		}
	}
	return true
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
	parts = append(parts, g.Status, ack, sil, g.Buzzed.String(), g.Pushed.String(), noteDigest(g))
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
	rows, err := b.d.VM.Query(ctx, fmt.Sprintf(`max by (location) (max_over_time(probe_success{job="probe",service="%s"}[3m]))`, svc))
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
		parts = append(parts, mark+" "+mdEscape(clean(r.Labels["location"], maxShort, false)))
	}
	return strings.Join(parts, "  ")
}

// card renders the group's card.
func (b *Bridge) card(ctx context.Context, g *store.Group) feishu.Card {
	return b.cardWithSessions(ctx, g, b.sessionSummary(g))
}

func (b *Bridge) cardWithSessions(ctx context.Context, g *store.Group, sessions cardSessions) feishu.Card {
	// A resolved card points to the retained incident without minting another share link.
	timelineURL := b.notificationPage(g)
	if g.Firing() {
		timelineURL = b.incidentPage(g)
	}
	now := b.now()
	firing := g.Firing()
	colour := feishu.Orange
	switch {
	case !firing:
		colour = feishu.Green
	case g.Ack.StopsEscalation(), g.Silence.Pauses(now):
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

	// Everything quoted into the card's markdown is text from outside Beacon or from people: escaped, it
	// mentions nobody and links nowhere (mdEscape).
	var md []string
	if d := g.Annotations["description"]; d != "" {
		md = append(md, mdEscape(clean(d, maxLong, true)))
	}
	if svc := b.service(g); svc != "" {
		md = append(md, b.t("**Service**: %s", mdEscape(cleanLine(svc))))
	} else if svcs := b.services(g); len(svcs) > 0 {
		const show = 10
		shown := make([]string, 0, show)
		for _, s := range svcs[:min(len(svcs), show)] {
			shown = append(shown, mdEscape(cleanLine(s)))
		}
		list := strings.Join(shown, ", ")
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
		by := mdEscape(cleanLine(g.Ack.By))
		if g.Ack.StopsEscalation() {
			state = append(state, b.t("Acknowledged by %s at %s via %s", by, b.clock(g.Ack.At), b.viaName(g.Ack.Via)))
		} else {
			state = append(state, b.t("Agent acknowledgement from %s at %s via %s", by, b.clock(g.Ack.At), b.viaName(g.Ack.Via)))
		}
		if g.Ack.Note != "" {
			state = append(state, b.t("Note: %s", mdEscape(clean(g.Ack.Note, maxLine, false))))
		}
	}
	switch {
	case g.Silence.Pauses(now):
		state = append(state, b.t("Silenced until %s by %s", b.clock(g.Silence.Until), mdEscape(cleanLine(g.Silence.By))))
	case g.Silence != nil && now.Before(g.Silence.Until):
		state = append(state, b.t("Silenced by agent %s until %s; people are still notified", mdEscape(cleanLine(g.Silence.By)), b.clock(g.Silence.Until)))
	}
	if !g.Buzzed.IsZero() {
		state = append(state, b.t("Buzzed in Feishu at %s", b.clock(g.Buzzed)))
	}
	if pushoverAccepted(g) {
		state = append(state, b.t("Pushover accepted the page at %s", b.clock(g.Pushed)))
	}

	els := []map[string]any{feishu.Markdown(strings.Join(md, "\n"))}
	if len(state) > 0 {
		els = append(els, feishu.Markdown("<font color='grey'>"+strings.Join(state, "\n")+"</font>"))
	}
	els = append(els, b.agentBlock(g), b.sessionsBlock(sessions), b.notesBlock(g))
	var links []map[string]any
	if timelineURL != "" {
		links = append(links, feishu.LinkButton(b.t("Timeline"), timelineURL))
	}
	if u := b.link(b.d.Config.Links.Runbook, g); u != "" {
		links = append(links, feishu.LinkButton(b.t("Runbook"), u))
	}
	if u := b.link(b.d.Config.Links.GrafanaService, g); u != "" {
		links = append(links, feishu.LinkButton(b.t("Dashboard"), u))
	}
	if firing {
		val := func(a string, extra ...string) map[string]any {
			v := map[string]any{"a": a, "g": g.ID}
			if g.Episode != "" {
				v["e"] = g.Episode
			}
			if len(extra) == 2 {
				v[extra[0]] = extra[1]
			}
			return v
		}
		var acts []map[string]any
		if !g.Ack.StopsEscalation() {
			acts = append(acts, feishu.Button(b.t("Acknowledge"), "primary_filled", val("ack")))
		}
		if !g.Silence.Pauses(now) { // a person may still silence what an agent has
			acts = append(acts,
				feishu.Button(b.t("Silence 1h"), "default", val("silence", "d", "1h")),
				feishu.Button(b.t("Silence 4h"), "default", val("silence", "d", "4h")),
				feishu.Button(b.t("Silence 24h"), "default", val("silence", "d", "24h")))
		}
		els = append(els, feishu.Hr(), feishu.Row(append(acts, links...)...))
		if !g.Ack.StopsEscalation() {
			els = append(els, feishu.NoteForm("ack_note", b.t("What are you doing about it?"), b.t("Acknowledge with note"), val("note")))
		}
	} else if len(links) > 0 {
		els = append(els, feishu.Hr(), feishu.Row(links...))
	}
	return feishu.NewCard(feishu.Header(b.title(g), subtitle, colour, tags...), els...)
}

// currentCardAction binds an action to an episode, or to its message for cards sent before episode
// binding. An SDK message ID, when present, must always name the current card.
func currentCardAction(g *store.Group, a feishu.Action) bool {
	if a.MessageID != "" && a.MessageID != g.MessageID {
		return false
	}
	if raw, ok := a.Value["e"]; ok {
		episode, _ := raw.(string)
		return episode != "" && episode == g.Episode
	}
	return a.MessageID != ""
}

// plainBody is the alert in a few lines of text (Pushover, Bark, the group bot). What it quotes cannot
// mention anyone in the chat (chatSafe).
func (b *Bridge) plainBody(g *store.Group) string {
	var lines []string
	if d := g.Annotations["description"]; d != "" {
		lines = append(lines, chatSafe(clean(d, maxLong, true)))
	}
	lines = append(lines, b.t("Since %s (%s)", b.clock(g.StartsAt), b.dur(b.now().Sub(g.StartsAt))))
	return strings.Join(lines, "\n")
}

func (b *Bridge) plainFiring(g *store.Group) string {
	lines := []string{b.t("Firing (%s): %s", b.severityName(g.Severity), chatSafe(cleanLine(b.title(g))))}
	if u := b.notificationPage(g); u != "" {
		lines = append(lines, u)
	}
	lines = append(lines, b.plainBody(g))
	if u := cleanURL(b.link(b.d.Config.Links.Runbook, g)); u != "" {
		lines = append(lines, u)
	}
	return strings.Join(lines, "\n")
}

func (b *Bridge) plainResolved(g *store.Group) string {
	text := b.t("Resolved: %s, after %s", chatSafe(cleanLine(b.title(g))), b.dur(g.ResolvedAt.Sub(g.StartsAt)))
	if u := b.notificationPage(g); u != "" {
		text += "\n" + u
	}
	return text
}

// notificationPage takes the recipient back to the alert after sign-in, without handing out read-only access.
func (b *Bridge) notificationPage(g *store.Group) string {
	base, err := url.Parse(b.d.Config.PublicURL)
	if err != nil {
		return ""
	}
	base.User, base.RawQuery, base.Fragment, base.ForceQuery = nil, "", "", false
	return cleanURL(strings.TrimSuffix(base.String(), "/") + link.SignInPath(link.IncidentPath(g.ID, g.Episode)))
}

func (b *Bridge) phoneLink(g *store.Group) (string, string) {
	if u := b.notificationPage(g); u != "" {
		return u, b.t("Open alert")
	}
	return cleanURL(b.link(b.d.Config.Links.Runbook, g)), b.t("Runbook")
}
