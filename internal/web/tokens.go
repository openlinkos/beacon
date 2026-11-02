package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// The tokens: who and what holds one, what it may do, which machines and SSH keys it has, and when it
// was made, expires and last renewed itself. Never a secret or its hash. An agent's token says whose
// agent it is (its scopes are the agent's, on the Agents page); a legacy token, from before accounts,
// can be claimed here by an owner as an agent of a person, keeping its secret. A token that reaches
// machines, and any agent's, can be revoked here by someone with machines:write (bridge.Revocable says
// why only those); machines' tokens stay with the admin socket.

type tokenLabels struct {
	Name, Scopes, Machines, Keys, Created, Expires, Renewed, Revoke, RevokeHint, Subject, Agent, Legacy, Claim, ClaimHint, Owner, AgentName,
	Preset, KeepScopes string
}

type tokenRow struct {
	Name, Subject, Created, CreatedBy, Expires, Renewed, Machines, Revoke, Agent, AgentHref, Claim, Closest string
	Scopes, Keys                                                                                            []string
	Expired, Self, Legacy                                                                                   bool
}

type tokensPage struct {
	shell
	L       tokenLabels
	List    list
	Rows    []tokenRow
	Note    string
	Owners  []option
	Presets []option
}

var tokensTmpl = parse("tokens.html")

func (s *Server) tokens(w http.ResponseWriter, r *http.Request, v *viewer) {
	all := s.Store.Tokens()
	sort.SliceStable(all, func(i, j int) bool { return all[i].Created.After(all[j].Created) })
	p := tokensPage{shell: s.shellFor(r, v, "tokens", s.t("Tokens")), L: tokenLabels{
		Name: s.t("Token"), Scopes: s.t("Scopes"), Machines: s.t("Machines"), Keys: s.t("SSH keys"), Created: s.t("Created"),
		Expires: s.t("Expires"), Renewed: s.t("Renewed"), Revoke: s.t("Revoke"),
		RevokeHint: s.t("Revoking ends the token at once: its SSH sessions are refused from then on and its console sign-ins end."),
		Subject:    s.t("Writes as"), Agent: s.t("Agent"), Legacy: s.t("Legacy"), Claim: s.t("Claim"),
		ClaimHint: s.t("Make it an agent of a person: it keeps its secret and expiry, and may do from then on what the agent's preset allows within that person's role."),
		Owner:     s.t("Owner"), AgentName: s.t("Agent name"), Preset: s.t("Preset"), KeepScopes: s.t("Keep its scopes (custom)"),
	}}
	p.Note = s.t("People's agents have their tokens on the Agents page. Machines' tokens are made with beacon enroll and beacon token create on the host and revoked there, since they keep monitoring running. A legacy token, from before accounts, keeps working; an owner can claim it as an agent of a person.")
	if v.owner() {
		for _, a := range s.Store.Accounts() {
			if !a.Disabled {
				p.Owners = append(p.Owners, option{Value: a.Username, Label: a.DisplayName + " (" + a.Username + ")"})
			}
		}
	}
	p.Presets = s.presetOptions("", "")
	p.Count = num(len(all))
	_, from, to := s.page(len(all), r.URL.Query(), consolePath("/tokens"), &p.List)
	for _, t := range all[from:to] {
		p.Rows = append(p.Rows, s.tokenRow(t, v))
	}
	s.render(w, tokensTmpl, p)
}

func (s *Server) tokenRow(t store.Token, v *viewer) tokenRow {
	row := tokenRow{Name: t.Name, Subject: t.Subject, Scopes: t.Scopes, Created: s.clock(t.Created), CreatedBy: t.CreatedBy,
		Expires: s.clock(t.Expires), Expired: !s.now().Before(t.Expires), Self: v.Token != nil && t.Name == v.Token.Name, Legacy: store.Legacy(t)}
	if !t.Renewed.IsZero() {
		row.Renewed = s.clock(t.Renewed)
	}
	if t.Agent != "" {
		row.Agent, row.AgentHref = t.Agent, agentHref(t.Agent)
		if a, ok := s.Store.Agent(t.Agent); ok {
			row.Agent = a.Owner + " / " + a.Name
		}
	}
	switch {
	case !t.Has("machines:ssh"):
	case len(t.Machines) == 0:
		row.Machines = s.t("All machines")
	default:
		row.Machines = strings.Join(t.Machines, ", ")
	}
	for _, k := range t.SSHKeys {
		row.Keys = append(row.Keys, k.Fingerprint)
	}
	if bridge.Revocable(t) && v.has("machines:write") {
		row.Revoke = consolePath("/tokens/") + url.PathEscape(t.Name) + "/revoke"
	}
	if row.Legacy && v.owner() {
		row.Claim, row.Closest = consolePath("/tokens/")+url.PathEscape(t.Name)+"/claim", store.ClosestPreset(t.Scopes)
	}
	return row
}

// Options lists the presets for a claim, the token's closest (custom: keep its scopes) selected.
func (r tokenRow) Options(p tokensPage) []option {
	out := []option{{Value: store.PresetCustom, Label: p.L.KeepScopes, Selected: r.Closest == store.PresetCustom}}
	for _, o := range p.Presets {
		o.Selected = o.Value == r.Closest
		out = append(out, o)
	}
	return out
}

func (s *Server) claim(w http.ResponseWriter, r *http.Request, v *viewer) {
	f := r.PostForm
	_, err := s.Bridge.ClaimToken(r.Context(), v.actor(), r.PathValue("name"), f.Get("owner"), strings.TrimSpace(f.Get("agent")), f.Get("preset"))
	switch {
	case errors.Is(err, store.ErrExists):
		back(w, r, consolePath("/tokens"), "error", "taken")
	case errors.Is(err, store.ErrNotFound):
		back(w, r, consolePath("/tokens"), "error", "not_found")
	case errors.Is(err, bridge.ErrBadInput):
		back(w, r, consolePath("/tokens"), "error", "not_legacy")
	case err != nil:
		s.failed(w, r, consolePath("/tokens"), "claim", err)
	default:
		back(w, r, consolePath("/tokens"), "done", "claimed")
	}
}
