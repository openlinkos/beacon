package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/store"
)

// The configuration Beacon holds, read-only: the catalogue (locations, heartbeats, resources), the
// target sets and the modules, and the history of its versions with who changed what, when, through
// what and why. A version's page shows what it changed, item by item, and offers to revert it to
// someone whose token has config:write. Changing the configuration is done through the API, MCP and
// the command line, which check every change as the files were checked.

type configLabels struct {
	Locations, Heartbeats, Resources, Targets, Modules, History, ID, Name, Region, Network, Standby, Expected string
	MaxAge, Severity, Description, Kind, Expires, Note, ProbedBy, Groups, TargetsN, Services, Prober, Version string
	Time, Who, Via, Action, Items, Yes, No, Empty, Older, Revert, RevertHint, RevertNote                      string
	Changed, Reporting                                                                                        string
}

func (s *Server) configLabels() configLabels {
	return configLabels{
		Locations: s.t("Probe locations"), Heartbeats: s.t("Heartbeats"), Resources: s.t("Resources"), Targets: s.t("Target sets"),
		Modules: s.t("Modules"), History: s.t("History"), ID: s.t("Id"), Name: s.t("Name"), Region: s.t("Region"), Network: s.t("Network"),
		Standby: s.t("Standby"), Expected: s.t("Expected"), MaxAge: s.t("Maximum age"), Severity: s.t("Severity"),
		Description: s.t("Description"), Kind: s.t("Kind"), Expires: s.t("Expires"), Note: s.t("Note"), ProbedBy: s.t("Probed by"),
		Groups: s.t("Groups"), TargetsN: s.t("Targets"), Services: s.t("Services"), Prober: s.t("Prober"), Version: s.t("Version"),
		Time: s.t("Time"), Who: s.t("Who"), Via: s.t("Via"), Action: s.t("Action"), Items: s.t("Items"), Yes: s.t("Yes"), No: s.t("No"),
		Empty: s.t("None"), Older: s.t("Older versions"), Revert: s.t("Revert this change"),
		RevertHint: s.t("Reverting gives every item this change touched its value from before, as a new version, if no later change touched it."),
		RevertNote: s.t("Why revert it"), Changed: s.t("What changed"),
		Reporting: s.t("Reporting"),
	}
}

func configHistoryFilter(limit int) configdb.HistoryFilter {
	return configdb.HistoryFilter{Limit: limit}
}

type locationRow struct {
	ID, Name, Region, Network, Reporting string
	Standby, Expected                    bool
}

type heartbeatRow struct{ Name, MaxAge, Severity, Description string }

type resourceRow struct{ Name, Kind, Expires, Note string }

type targetSetRow struct {
	Name, ProbedBy, Groups, Targets, Modules, Services string
}

type moduleRow struct{ Name, Prober string }

type versionRow struct {
	Version, Href, Time, Who, Via, Action, Note, Items string
}

type configPage struct {
	shell
	L          configLabels
	Locations  []locationRow
	Heartbeats []heartbeatRow
	Resources  []resourceRow
	Targets    []targetSetRow
	Modules    []moduleRow
	History    []versionRow
	Older      string
}

var configTmpl = parse("config.html")

func (s *Server) configPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := configPage{shell: s.shellFor(r, v, "config", s.t("Configuration")), L: s.configLabels()}
	if s.Config == nil {
		s.render(w, configTmpl, p)
		return
	}
	p.Count = s.t("version %d", s.Config.Version())
	view, err := s.Config.Get("", "")
	if err != nil {
		s.log().Error("console: reading the configuration failed", "err", err)
	}
	cat, now := s.Config.Catalog(), s.now()
	var seen map[string]bool
	if s.Reporting != nil {
		seen = map[string]bool{}
		for who, at := range s.Reporting() {
			seen[who] = now.Sub(at) < reportingWindow
		}
	}
	for _, l := range view.Locations {
		row := locationRow{ID: l.ID, Name: l.Name, Region: l.Region, Network: l.Network, Standby: l.Standby, Expected: cat.Expected(l, now)}
		if seen != nil {
			row.Reporting = p.L.No
			if seen[l.ID] {
				row.Reporting = p.L.Yes
			}
		}
		p.Locations = append(p.Locations, row)
	}
	for _, h := range view.Heartbeats {
		p.Heartbeats = append(p.Heartbeats, heartbeatRow{Name: h.Name, MaxAge: h.MaxAge.D().String(), Severity: s.severity(h.Severity), Description: h.Description})
	}
	for _, x := range view.Resources {
		p.Resources = append(p.Resources, resourceRow{Name: x.Name, Kind: x.Kind, Expires: x.Expires, Note: x.Note})
	}
	for _, ts := range view.Targets {
		n, modules, services := 0, map[string]bool{}, map[string]bool{}
		for _, g := range ts.Groups {
			n += len(g.Targets)
			modules[g.Labels["module"]] = true
			services[g.Labels["service"]] = true
		}
		p.Targets = append(p.Targets, targetSetRow{Name: ts.Name, ProbedBy: ts.ProbedBy, Groups: num(len(ts.Groups)), Targets: num(n),
			Modules: joinKeys(modules), Services: joinKeys(services)})
	}
	for _, name := range sortedNames(view.Modules) {
		row := moduleRow{Name: name}
		if m, ok := view.Modules[name].(map[string]any); ok {
			row.Prober, _ = m["prober"].(string)
		}
		p.Modules = append(p.Modules, row)
	}
	f := configHistoryFilter(perPage)
	if b, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil && b > 1 {
		f.Before = b
	}
	hist, _ := s.Config.History(f)
	for _, ch := range hist {
		p.History = append(p.History, s.versionRow(ch))
	}
	if len(hist) == perPage && hist[len(hist)-1].Version > 1 {
		p.Older = consolePath("/config?before=") + strconv.FormatInt(hist[len(hist)-1].Version, 10) + "#history"
	}
	s.render(w, configTmpl, p)
}

func (s *Server) versionRow(ch store.ConfigChange) versionRow {
	row := versionRow{Version: strconv.FormatInt(ch.Version, 10), Href: consolePath("/config/history/") + strconv.FormatInt(ch.Version, 10),
		Time: s.clock(ch.Time), Who: ch.Actor, Via: s.via(ch.Via), Action: ch.Action, Note: ch.Note}
	var items []string
	for _, it := range ch.Items {
		items = append(items, it.Op+" "+it.Kind+" "+it.Key)
	}
	row.Items = shorten(strings.Join(items, ", "), 200)
	if ch.Reverts > 0 {
		row.Action = ch.Action + " " + strconv.FormatInt(ch.Reverts, 10)
	}
	return row
}

func joinKeys(m map[string]bool) string {
	var out []string
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return shorten(strings.Join(out, ", "), 120)
}

func sortedNames(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- one version

type diffLine struct{ Op, Text string } // Op: "+", "-" or " "

type itemDiff struct {
	Kind, Key, Op string
	Lines         []diffLine
}

type versionPage struct {
	shell
	L       configLabels
	Change  versionRow
	Reverts string
	Items   []itemDiff
	Revert  string // where the revert form posts; "" leaves it out
}

var versionTmpl = parse("version.html")

func (s *Server) versionPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	n, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || n < 1 || s.Config == nil {
		s.missing(w, r, v)
		return
	}
	ch, err := s.Config.Change(n)
	if err != nil {
		s.missing(w, r, v)
		return
	}
	l := s.configLabels()
	p := versionPage{shell: s.shellFor(r, v, "config", s.t("Version %d", n), crumb{Label: s.t("Configuration"), Href: consolePath("/config")}), L: l,
		Change: s.versionRow(ch)}
	p.Badges = []badge{{Text: ch.Action}}
	if ch.Reverts > 0 {
		p.Reverts = s.t("Reverts version %d", ch.Reverts)
	}
	for _, it := range ch.Items {
		p.Items = append(p.Items, itemDiff{Kind: it.Kind, Key: it.Key, Op: it.Op, Lines: diff(pretty(it.Before), pretty(it.After))})
	}
	if v.has("config:write") {
		p.Revert = consolePath("/config/history/") + url.PathEscape(strconv.FormatInt(n, 10)) + "/revert"
	}
	s.render(w, versionTmpl, p)
}

// pretty lays a value out one field to a line, so that a diff shows what changed; nil is no lines.
func pretty(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return strings.Split(string(raw), "\n")
	}
	return strings.Split(buf.String(), "\n")
}

// maxDiff bounds the work of a diff; longer values are shown whole, before then after.
const maxDiff = 4000

// diff is the lines of a and b in order, marked removed (-), added (+) or kept, by their longest
// common subsequence.
func diff(a, b []string) []diffLine {
	if len(a)*len(b) > maxDiff*maxDiff/16 { // a million cells at most
		var out []diffLine
		for _, x := range a {
			out = append(out, diffLine{"-", x})
		}
		for _, x := range b {
			out = append(out, diffLine{"+", x})
		}
		return out
	}
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []diffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffLine{" ", a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffLine{"-", a[i]})
			i++
		default:
			out = append(out, diffLine{"+", b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffLine{"-", a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffLine{"+", b[j]})
	}
	return out
}
