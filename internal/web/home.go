package web

import (
	"net/http"
	"strings"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/web/link"
)

// The site's root, and the console's old paths. / is public: the default status page, as /status
// serves it, unless that page has a host name of its own (it lives there, and Beacon's own host is
// then the console's); otherwise a landing page that presents Beacon's architecture, capabilities,
// status pages and console entry. The console was at the root until 0.3.0; its old paths send people
// on to /console, and the links handed out until then (signed for /alerts/{id} and /sessions/{id})
// keep working until they expire.

var landingTmpl = parseStatus("landing.html")

type landingFeature struct {
	Icon, Tag, Title, Text string
}

type landingStep struct {
	Number, Badge, Tone, Text string
}

type landingAudience struct {
	Icon, Eyebrow, Title, Lead string
	Points                     []string
}

type landingSnippet struct {
	Icon, Title, Label, Code string
}

type landingView struct {
	Badge         string
	Subline       string
	DocsURL       string
	DocsLabel     string
	RepoURL       string
	RepoLabel     string
	Specs         []string
	LoopTitle     string
	LoopTag       string
	LoopSteps     []landingStep
	PillarsTitle  string
	PillarsLead   string
	Pillars       []landingFeature
	DualTitle     string
	DualLead      string
	Audiences     []landingAudience
	ConnectTitle  string
	ConnectLead   string
	Snippets      []landingSnippet
	PagesTitle    string
	PagesLead     string
	OpenPageLabel string
	NavFeatures   string
	NavWorkflow   string
	NavConnect    string
	NavStatus     string
}

// home is the site's root.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if s.Status != nil {
		if def, ok := s.Status.Def(""); ok && def.Host == "" {
			s.statusPage(w, r, "", false)
			return
		}
	}
	s.landing(w, r)
}

func (s *Server) publicBaseURL(r *http.Request) string {
	if s.Pages != nil && s.Pages.PublicURL != "" {
		return strings.TrimRight(s.Pages.PublicURL, "/")
	}
	if r != nil && r.Host != "" {
		scheme := "https"
		if strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "127.0.0.1") {
			scheme = "http"
		}
		return scheme + "://" + r.Host
	}
	return "https://status.example.com"
}

// landing is the page at / without a status page there, showcasing the platform, published status pages
// and the way into the console and MCP server.
func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	l := s.Lang
	switch r.URL.Query().Get("lang") {
	case "zh", "zh-CN":
		l = i18n.ZH
	case "en":
		l = i18n.EN
	}
	name := s.Branding.Name()
	v := &statusView{Lang: l.HTML(), Title: name, Name: name, Brand: "beacon", Version: s.version(), Icons: statusSprite,
		L: statusLabelsIn(l), Root: "/", Description: i18n.T(l, "Self-hosted monitoring and SSH access for people and their AI agents.")}
	v.Mark, v.MarkHero = marks(v.Brand)
	v.SignIn, v.private = s.consoleLink(r, l, false)
	v.Powered = s.poweredBy(l)
	for _, code := range []string{"en", "zh"} {
		lg := i18n.Lang(code)
		ll := langLink{Code: lg.HTML(), Name: lg.Name(), Href: "/?lang=" + code, HrefLang: lg.HTML(), Current: lg == l}
		v.Langs = append(v.Langs, ll)
		if ll.Current {
			v.LangName = ll.Name
		}
	}
	for _, p := range s.Status.Pages() {
		v.Pages = append(v.Pages, linkView{Title: p.Title.In(string(l)), URL: s.Status.URL(p, "")})
	}
	baseURL := s.publicBaseURL(r)
	mcpURL := baseURL + "/mcp"
	lv := &landingView{
		Badge:     i18n.T(l, "Human & AI operations infrastructure"),
		Subline:   i18n.T(l, "Multi-location consensus probing, short-lived SSH certificates with browser replay, and a native MCP server — built as one compact Go service you control."),
		Specs:     []string{i18n.T(l, "Multi-location consensus"), i18n.T(l, "5-min SSH certificates"), i18n.T(l, "Native MCP + OAuth 2.1"), i18n.T(l, "Recorded replays & audit")},
		LoopTitle: i18n.T(l, "Autonomous incident loop"),
		LoopTag:   "MCP /mcp",
		LoopSteps: []landingStep{
			{Number: "01", Badge: i18n.T(l, "Probe"), Tone: "danger", Text: i18n.T(l, "Edge probes reach quorum: api.example.com failed with tls_expired across multiple locations.")},
			{Number: "02", Badge: i18n.T(l, "Wake"), Tone: "info", Text: i18n.T(l, "Signed webhook wakes your AI agent; get_evidence returns probe reasons, changes and machines in one call.")},
			{Number: "03", Badge: i18n.T(l, "SSH"), Tone: "warning", Text: i18n.T(l, "Agent connects via 5-minute certificate without holding keys; commands and terminal output are recorded.")},
			{Number: "04", Badge: i18n.T(l, "Verify"), Tone: "success", Text: i18n.T(l, "probe_now re-checks every location on demand, posts the handover note and updates public status.")},
		},
		PillarsTitle: i18n.T(l, "Engineered for clarity and control"),
		PillarsLead:  i18n.T(l, "Four core capabilities working on one shared identity model, without lock-in to any model or cloud."),
		Pillars: []landingFeature{
			{
				Icon: "activity", Tag: i18n.T(l, "Monitoring"),
				Title: i18n.T(l, "Consensus probing from user networks"),
				Text:  i18n.T(l, "Lightweight probes under 20 MB test HTTP, TLS, TCP, Git, container registries and domain expiry with exact failure reasons. A single node hiccup disturbs nobody."),
			},
			{
				Icon: "terminal", Tag: i18n.T(l, "SSH channel"),
				Title: i18n.T(l, "Zero-key server access and replay"),
				Text:  i18n.T(l, "Reach direct hosts or servers behind NAT using native ssh, scp, rsync or MCP tools. Every session uses a 5-minute certificate and records an asciicast replay."),
			},
			{
				Icon: "bot", Tag: i18n.T(l, "Agent-native"),
				Title: i18n.T(l, "First-class MCP server and REST API"),
				Text:  i18n.T(l, "Six structured operational domains with live capability discovery, read-only resources, operational prompts, OAuth 2.1 browser sign-in and full OpenAPI parity."),
			},
			{
				Icon: "shield-check", Tag: i18n.T(l, "Governance"),
				Title: i18n.T(l, "Visibility instead of approval bottlenecks"),
				Text:  i18n.T(l, "Agents never exceed their owner's role. Review every command and configuration diff in the hash-chained audit log, or halt all agent SSH with one pause switch."),
			},
		},
		DualTitle: i18n.T(l, "One workspace for people and their agents"),
		DualLead:  i18n.T(l, "People see clean timelines, replays and handovers; agents speak standard SSH, HTTP and MCP out of the box."),
		Audiences: []landingAudience{
			{
				Icon: "users", Eyebrow: i18n.T(l, "For teams"),
				Title: i18n.T(l, "Calm operational visibility"),
				Lead:  i18n.T(l, "Take charge of alerts from Feishu, Slack or your phone, inspect evidence and replay every terminal session in your browser."),
				Points: []string{
					i18n.T(l, "Unified incident cockpit with probe matrix, timeline and handover notes"),
					i18n.T(l, "Personal SSH access recorded under your own account identity"),
					i18n.T(l, "Public status pages with 90-day uptime history and planned maintenance"),
				},
			},
			{
				Icon: "zap", Eyebrow: i18n.T(l, "For AI agents"),
				Title: i18n.T(l, "Familiar tools, zero guesswork"),
				Lead:  i18n.T(l, "Claude Code, Codex and any MCP client discover capabilities automatically and operate machines safely within clear boundaries."),
				Points: []string{
					i18n.T(l, "describe_capabilities reveals domains, scopes and enabled features in one call"),
					i18n.T(l, "Built-in run_command, search_files, read_file, write_file and edit_file tools"),
					i18n.T(l, "Live beacon:// MCP resources and guided triage, recovery and handover prompts"),
				},
			},
		},
		ConnectTitle: i18n.T(l, "Connect your agent in seconds"),
		ConnectLead:  i18n.T(l, "Sign in through the browser with MCP OAuth — no static secrets copied between terminals — or query the REST API directly."),
		Snippets: []landingSnippet{
			{Icon: "bot", Title: "Claude Code", Label: i18n.T(l, "MCP over HTTP + OAuth"), Code: "claude mcp add --scope user --transport http beacon " + mcpURL},
			{Icon: "terminal", Title: "Codex CLI", Label: i18n.T(l, "MCP over HTTP + OAuth"), Code: "codex mcp add beacon --url " + mcpURL},
			{Icon: "layers", Title: i18n.T(l, "Capability discovery"), Label: "REST / OpenAPI 3.1", Code: "curl -H \"Authorization: Bearer $BEACON_TOKEN\" " + baseURL + "/api/v1/capabilities"},
		},
		PagesTitle:    i18n.T(l, "Published status pages"),
		PagesLead:     i18n.T(l, "Live service availability and incident updates served by this deployment."),
		OpenPageLabel: i18n.T(l, "View status page"),
		NavFeatures:   i18n.T(l, "Capabilities"),
		NavWorkflow:   i18n.T(l, "People & Agents"),
		NavConnect:    i18n.T(l, "MCP & API"),
		NavStatus:     i18n.T(l, "Status pages"),
	}
	if v.Powered != nil {
		lv.DocsURL = v.Powered.Docs
		lv.DocsLabel = i18n.T(l, "Documentation")
		lv.RepoURL = v.Powered.Repo
		lv.RepoLabel = v.Powered.RepoLabel
	}
	v.Landing = lv
	s.renderPublic(w, r, landingTmpl, v)
}

// consoleLink is the public header's way into the console: Console for someone signed in, Sign in for
// anyone else, both to /console, which sends whoever is not signed in to sign in. There is none on a
// status page's own host, where the console does not answer, nor without the console. Private says the
// link names the reader's sign-in.
func (s *Server) consoleLink(r *http.Request, l i18n.Lang, onHost bool) (_ *linkView, private bool) {
	if onHost || !s.Console() {
		return nil, false
	}
	if s.viewer(r) != nil {
		return &linkView{Title: i18n.T(l, "Console"), URL: consolePath("")}, true
	}
	return &linkView{Title: i18n.T(l, "Sign in"), URL: consolePath("")}, false
}

// movedPaths are the console's pages as they were before it moved under /console.
var movedPaths = []string{"/sign-in", "/alerts", "/alerts/{id}", "/machines", "/machines/{name}", "/sessions", "/sessions/{id}",
	"/sessions/{id}/recording", "/tokens", "/config", "/config/history/{version}", "/agents", "/audit", "/changes"}

func (s *Server) registerMoved(mux *http.ServeMux) {
	for _, p := range movedPaths {
		mux.HandleFunc("GET "+p, s.moved)
	}
}

// moved sends a request for an old path on to the page's place under /console, with its query. A
// signed link to an alert's or a session's page names the old path inside its signature: one that
// verifies there goes on with a signature for the new path that expires when the old one does. Any
// other signature (forged, expired, for another path) is dropped, and the page then judges the request
// as it does any other: a plain 404, unless the reader is signed in or sends a token.
func (s *Server) moved(w http.ResponseWriter, r *http.Request) {
	headers(w)
	w.Header().Set("Cache-Control", "no-store")
	to, query := consolePath(r.URL.EscapedPath()), r.URL.RawQuery
	if r.URL.Path == "/agents" { // the hooks' page, which was called Agents before agents had pages of their own
		to = consolePath("/hooks")
	}
	if q := r.URL.Query(); q.Has(link.Param) {
		sig := q.Get(link.Param)
		q.Del(link.Param)
		if now, ok := link.Moved(r.URL.Path); ok && s.Pages != nil && s.Pages.Signer != nil {
			if grant, ok := s.Pages.Signer.VerifyGrant(http.MethodGet, r.URL.Path, sig); ok {
				if movedSignature := s.Pages.Signer.Derive(http.MethodGet, now, grant); movedSignature != "" {
					q.Set(link.Param, movedSignature)
				}
			}
		}
		query = q.Encode()
	}
	if query != "" {
		to += "?" + query
	}
	http.Redirect(w, r, to, http.StatusPermanentRedirect) //nolint:gosec // G710: always a path under /console on this site
}
