package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/openlinkos/beacon/internal/secrets"
)

func (s *Server) registerCredentials(at func(string, string, http.HandlerFunc)) {
	if s.Credentials == nil && !s.CredentialsConfigured {
		return
	}
	at("GET", "/credentials", s.console(secrets.ManageScope, s.credentialsPage))
	at("GET", "/credentials/{id}", s.console(secrets.ManageScope, s.credentialsPage))
	at("POST", "/credentials", s.action(secrets.ManageScope, s.writeCredential))
	at("POST", "/credentials/{id}/{operation}", s.action(secrets.ManageScope, s.writeCredential))
}

type credentialLabels struct {
	Add, Name, Value, NewValue, Endpoint, Help, None, Items, Updated, Context, Grant     string
	Agent, Owner, Until, Expires, Save, Rotate, Revoke, Test, Record, NoAgents, NoGrants string
}

type credentialGrantRow struct {
	secrets.Grant
	Ends string
}

type credentialPage struct {
	shell                                    `json:"-"`
	L                                        credentialLabels
	Base, Audit, Rotate, Grant, Revoke, Test string
	Rows                                     []secrets.Credential
	Selected                                 *secrets.Credential
	Grants                                   []credentialGrantRow
	Agents, Durations                        []option
}

var credentialsTmpl = parse("credentials.html")

func (s *Server) credentialsPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	if !v.person() {
		s.forbidden(w, r, v, secrets.ManageScope)
		return
	}
	if s.Credentials == nil {
		s.credentialsUnavailable(w, r, v)
		return
	}
	items, err := s.Credentials.List(v.Principal.InConsoleSession(v.ID))
	if errors.Is(err, secrets.ErrAnchorPending) {
		s.credentialsPending(w, r, v)
		return
	}
	if err != nil {
		s.message(w, r, v, http.StatusServiceUnavailable, s.t("Team credentials"), s.t("The team credential store cannot be verified."))
		return
	}
	p := credentialPage{shell: s.shellFor(r, v, "credentials", s.t("Team credentials")), Rows: items, Base: consolePath("/credentials")}
	p.L = credentialLabels{
		Add: s.t("Add a credential"), Name: s.t("Credential name"), Value: s.t("Bearer token"), NewValue: s.t("Replacement bearer token"),
		Endpoint: s.t("HTTPS endpoint"), Help: s.t("Use an approved HTTPS endpoint. Each use sends the token there and returns only the HTTP status."),
		None: s.t("No team credentials yet."), Items: s.t("Credentials"), Updated: s.t("Updated"),
		Context: s.t("The endpoint stays fixed. Replacing its token keeps existing grants."),
		Grant:   s.t("Delegate use"), Agent: s.t("Agent"), Owner: s.t("Owner"), Until: s.t("Duration"), Expires: s.t("Expires"),
		Save: s.t("Save credential"), Rotate: s.t("Replace token"), Revoke: s.t("Revoke grant"), Test: s.t("Send test request"), Record: s.t("View audit entries"),
		NoAgents: s.t("Give an agent the secrets:use scope before delegating to it."), NoGrants: s.t("No agent grants for this credential."),
	}
	id := r.PathValue("id")
	if id != "" {
		i := slices.IndexFunc(items, func(c secrets.Credential) bool { return c.ID == id })
		if i < 0 {
			s.missing(w, r, v)
			return
		}
		p.Selected = &items[i]
		base := p.Base + "/" + id
		p.Rotate, p.Grant, p.Revoke, p.Test = base+"/rotate", base+"/grant", base+"/revoke", base+"/test"
		p.Audit = consolePath("/audit") + "?q=" + url.QueryEscape(id)
		p.Heading = p.Selected.Name
		p.Title = p.Heading + " – Beacon"
		p.Crumbs = append(p.Crumbs, crumb{Label: s.t("Team credentials"), Href: p.Base})
		for _, g := range p.Selected.Grants {
			p.Grants = append(p.Grants, credentialGrantRow{Grant: g, Ends: s.clock(g.Until)})
		}
		for _, agent := range s.Credentials.AgentChoices() {
			p.Agents = append(p.Agents, option{Value: agent, Label: agent})
		}
		for _, hrs := range []int64{1, 8, 24, 168, 720, 2160} {
			p.Durations = append(p.Durations, option{Value: strconv.FormatInt(hrs, 10), Label: s.dur(time.Duration(hrs) * time.Hour), Selected: hrs == 24})
		}
	}
	if value, err := strconv.Atoi(r.URL.Query().Get("status")); err == nil && value >= 100 && value <= 599 {
		p.Toast = s.t("Service returned HTTP %d", value)
		p.ToastTone = "info"
	}
	s.render(w, credentialsTmpl, p)
}

func (s *Server) writeCredential(w http.ResponseWriter, r *http.Request, v *viewer) {
	if !v.person() {
		s.forbidden(w, r, v, secrets.ManageScope)
		return
	}
	if s.Credentials == nil {
		s.credentialsUnavailable(w, r, v)
		return
	}
	const via = "console"
	target := consolePath("/credentials")
	id := r.PathValue("id")
	if id != "" {
		target += "/" + url.PathEscape(id)
	}
	var err error
	ctx, p := r.Context(), v.Principal.InConsoleSession(v.ID)
	version, _ := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	switch op := r.PathValue("operation"); op {
	case "":
		var next secrets.Credential
		next, err = s.Credentials.Create(ctx, p, via, secrets.NewCredential{Name: r.PostForm.Get("name"),
			Endpoint: r.PostForm.Get("endpoint"), Value: r.PostForm.Get("value")})
		if err == nil {
			target += "/" + next.ID
		}
	case "rotate":
		_, err = s.Credentials.Rotate(ctx, p, via, id, version, r.PostForm.Get("value"))
	case "grant":
		hrs, _ := strconv.ParseInt(r.PostForm.Get("hours"), 10, 32)
		if hrs < 1 || hrs > 2160 {
			err = secrets.ErrInput
		} else {
			_, err = s.Credentials.SetGrant(ctx, p, via, id, version, r.PostForm.Get("agent"), s.now().Add(time.Duration(hrs)*time.Hour))
		}
	case "revoke":
		_, err = s.Credentials.Revoke(ctx, p, via, id, version, r.PostForm.Get("agent"))
	case "test":
		var out secrets.Result
		out, err = s.Credentials.Use(ctx, p, via, id)
		if err == nil {
			back(w, r, target, "status", strconv.Itoa(out.Status))
			return
		}
	default:
		s.missing(w, r, v)
		return
	}
	switch {
	case errors.Is(err, secrets.ErrAnchorPending):
		s.credentialsPending(w, r, v)
	case errors.Is(err, secrets.ErrAccess):
		back(w, r, target, "error", "credential_denied")
	case errors.Is(err, secrets.ErrInput):
		back(w, r, target, "error", "credential_invalid")
	case errors.Is(err, secrets.ErrConflict):
		back(w, r, target, "error", "credential_changed")
	case errors.Is(err, secrets.ErrUpstream):
		back(w, r, target, "error", "credential_upstream")
	case err != nil:
		s.log().Error("team credential action could not be recorded")
		back(w, r, target, "error", "credential_unavailable")
	default:
		back(w, r, target, "done", "saved")
	}
}

func (s *Server) credentialsPending(w http.ResponseWriter, r *http.Request, v *viewer) {
	s.message(w, r, v, http.StatusServiceUnavailable, s.t("Credential change is awaiting verification"),
		s.t("Beacon wrote the change to the database. Check the credential store and reload before trying again."))
}

func (s *Server) credentialsUnavailable(w http.ResponseWriter, r *http.Request, v *viewer) {
	s.message(w, r, v, http.StatusServiceUnavailable, s.t("Team credentials"), s.t("The team credential store cannot be verified."))
}
