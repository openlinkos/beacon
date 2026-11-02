package web

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// The tokens: who and what holds one, what it may do, which machines and SSH keys it has, and when it
// was made, expires and last renewed itself. Never a secret or its hash. A token that reaches machines
// can be revoked here by someone whose token has machines:write (bridge.Revocable says why only those).

type tokenLabels struct {
	Name, Scopes, Machines, Keys, Created, Expires, Renewed, Revoke, RevokeHint, Subject string
}

type tokenRow struct {
	Name, Subject, Created, CreatedBy, Expires, Renewed, Machines, Revoke string
	Scopes, Keys                                                          []string
	Expired, Self                                                         bool
}

type tokensPage struct {
	shell
	L    tokenLabels
	List list
	Rows []tokenRow
	Note string
}

var tokensTmpl = parse("tokens.html")

func (s *Server) tokens(w http.ResponseWriter, r *http.Request, v *viewer) {
	all := s.Store.Tokens()
	sort.SliceStable(all, func(i, j int) bool { return all[i].Created.After(all[j].Created) })
	p := tokensPage{shell: s.shellFor(r, v, "tokens", s.t("Tokens")), L: tokenLabels{
		Name: s.t("Token"), Scopes: s.t("Scopes"), Machines: s.t("Machines"), Keys: s.t("SSH keys"), Created: s.t("Created"),
		Expires: s.t("Expires"), Renewed: s.t("Renewed"), Revoke: s.t("Revoke"),
		RevokeHint: s.t("Revoking ends the token at once: its SSH sessions are refused from then on and its console sign-ins end."),
		Subject:    s.t("Writes as"),
	}}
	p.Note = s.t("Tokens are made with beacon token create on the host. A token that reaches machines can be revoked here with machines:write, to stop one agent; the others are revoked with beacon token revoke on the host, since they keep monitoring running.")
	p.Count = num(len(all))
	_, from, to := s.page(len(all), r.URL.Query(), consolePath("/tokens"), &p.List)
	for _, t := range all[from:to] {
		p.Rows = append(p.Rows, s.tokenRow(t, v))
	}
	s.render(w, tokensTmpl, p)
}

func (s *Server) tokenRow(t store.Token, v *viewer) tokenRow {
	row := tokenRow{Name: t.Name, Subject: t.Subject, Scopes: t.Scopes, Created: s.clock(t.Created), CreatedBy: t.CreatedBy,
		Expires: s.clock(t.Expires), Expired: !s.now().Before(t.Expires), Self: t.Name == v.Token.Name}
	if !t.Renewed.IsZero() {
		row.Renewed = s.clock(t.Renewed)
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
	return row
}
