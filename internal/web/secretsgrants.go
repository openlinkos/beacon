package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/store"
)

// The console's grants pages: the waiting requests with their decisions, the grants with their
// revocation, the entitlements with the agent tags, and an agent's own secret scope. The people
// who decide here are the operator and owner roles and the reviewers an entitlement names; the
// vault's own checks hold, whichever door the decision comes through.

// secretsUI is what the grants' pages need of the vault, an interface so the pages can be tested
// against a stub.
type secretsUI interface {
	GrantsOf(agent string) []secrets.Grant
	Request(id string) (secrets.Request, bool)
	Requests(state string) []secrets.Request
	Decide(who secrets.DecideActor, requestID string, approve bool, note string, uses, duration *int64) (secrets.Request, secrets.Grant, error)
	AllGrants(state string) []secrets.Grant
	RevokeGrant(who secrets.Actor, id, reason string) error
	Entitlements() []secrets.Entitlement
	SetEntitlement(who secrets.Actor, e secrets.Entitlement) (secrets.Entitlement, error)
	DeleteEntitlement(who secrets.Actor, name string) error
	TagsOfAgent(agent string) []string
	SetAgentTags(who secrets.Actor, agent string, tags []string) error
	PreviewForAgent(agent string) secrets.ScopePreview
	PreviewForTemplates(names []string) secrets.ScopePreview
	EntitlementsOfAgent(agent string) []secrets.Entitlement
	TemplateEntitlements() []secrets.Entitlement
	ApplyTemplates(who secrets.Actor, agent string, names []string) error
	SuspendedAgents() map[string]string
	UnsuspendAgent(who secrets.Actor, agent string) error
	AgentOwner(name string) (string, bool)
}

var (
	secretApprovalsTmpl = parse("secretsapprovals.html")
	secretGrantsTmpl    = parse("secretsgrants.html")
	secretPoliciesTmpl  = parse("secretspolicies.html")
	agentSecretsTmpl    = parse("agentsecrets.html")
)

// registerSecretPages adds the grants' pages and forms under the console.
func (s *Server) registerSecretPages(at func(method, path string, h http.HandlerFunc)) {
	at("GET", "/secrets/approvals", s.console("", s.secretApprovalsPage))
	at("POST", "/secrets/requests/{id}/decide", s.action("", s.secretDecideForm))
	at("GET", "/secrets/grants", s.console("", s.secretGrantsPage))
	at("POST", "/secrets/grants/{id}/revoke", s.action("secrets:admin", s.secretRevokeForm))
	at("GET", "/secrets/policies", s.console("secrets:admin", s.secretPoliciesPage))
	at("POST", "/secrets/entitlement", s.action("secrets:admin", s.secretEntitlementForm))
	at("POST", "/secrets/entitlement/delete", s.action("secrets:admin", s.secretEntitlementDeleteForm))
	at("POST", "/secrets/agent-tags", s.action("secrets:admin", s.secretAgentTagsForm))
	at("GET", "/agents/{name}/secrets", s.console("", s.agentSecretsPage))
	at("POST", "/secrets/agents/{name}/templates", s.action("", s.secretTemplatesForm))
	at("POST", "/secrets/agents/{name}/unsuspend", s.action("secrets:admin", s.secretUnsuspendForm))
}

// secretLabels are the pages' own strings, rendered in the console's language.
type secretLabels struct {
	Approvals, ApprovalsHint, Waiting, Decided, Agent, Items, Reason, Level, Uses, Duration,
	Decide, Approve, ApproveOnce, Deny, Note, NoteHint, Grants, GrantsHint, AgentCol, Item,
	Left, Expires, Status, Approver, Revoke, RevokeReason, Entitlements, EntitlementsHint,
	NewEntitlement, Name, Description, Principals, Agents, AgentTags, AgentsOf, Resources,
	Collections, TagPatterns, Fields, Actions, ApprovalFloor, Reviewers, Template, ExpiresAt,
	Save, Delete, AgentTagging, AgentTaggingHint, Set, Scope, ScopeHint, Preview, Total, Auto,
	Review, Person, Never, Templates, TemplatesHint, Apply, Suspended, SuspendedHint, Lift,
	Unlimited, NoExpiry, None, Pending, Granted, Denied, Expired, Withdrawn, Active, Exhausted,
	Revoked, Released, Request, ChangedAt, Yes, No string
}

func (s *Server) secretLabels() secretLabels {
	return secretLabels{
		Approvals: s.t("Secret approvals"), ApprovalsHint: s.t("The requests waiting for a person's decision, and how they ended."),
		Waiting: s.t("Waiting"), Decided: s.t("Decided"), Agent: s.t("Agent"), Items: s.t("Items"),
		Reason: s.t("Reason"), Level: s.t("Level"), Uses: s.t("Uses"), Duration: s.t("For"),
		Decide: s.t("Decide"), Approve: s.t("Approve as asked"), ApproveOnce: s.t("Approve once"), Deny: s.t("Deny"),
		Note: s.t("The decision's reason"), NoteHint: s.t("A decision says why."),
		Grants: s.t("Grants"), GrantsHint: s.t("Every grant, live and ended."),
		AgentCol: s.t("Agent"), Item: s.t("Item"), Left: s.t("Uses left"), Expires: s.t("Until"),
		Status: s.t("Status"), Approver: s.t("Approved by"), Revoke: s.t("Revoke"), RevokeReason: s.t("Why"),
		Entitlements: s.t("Entitlements"), EntitlementsHint: s.t("Who may ask for what: the second axis of the vault's policy."),
		NewEntitlement: s.t("New entitlement"), Name: s.t("Name"), Description: s.t("Description"),
		Principals: s.t("Principals"), Agents: s.t("Agents"), AgentTags: s.t("Agent tags"), AgentsOf: s.t("Agents of"),
		Resources: s.t("Resources"), Collections: s.t("Collections"), TagPatterns: s.t("Tag patterns"),
		Fields: s.t("Fields"), Actions: s.t("Actions"), ApprovalFloor: s.t("Approval floor"), Reviewers: s.t("Reviewers"),
		Template: s.t("Template"), ExpiresAt: s.t("Expires"), Save: s.t("Save"), Delete: s.t("Delete"),
		AgentTagging: s.t("Agent grouping tags"), AgentTaggingHint: s.t("role/on-call and the like; entitlements reach agents by them."),
		Set: s.t("Set"), Scope: s.t("Secret scope"), ScopeHint: s.t("What this agent can reach, and what it holds."),
		Preview: s.t("It could see"), Total: s.t("items"), Auto: s.t("auto"), Review: s.t("review"), Person: s.t("person"),
		Never: s.t("never (invisible)"), Templates: s.t("Templates"), TemplatesHint: s.t("Hand this agent the template entitlements named."),
		Apply: s.t("Apply"), Suspended: s.t("Suspended agents"), SuspendedHint: s.t("An anomaly rule stopped these agents' grants; an operator lifts it."),
		Lift: s.t("Lift"), Unlimited: s.t("unlimited"), NoExpiry: s.t("no expiry"), None: s.t("None"),
		Pending: s.t("waiting"), Granted: s.t("granted"), Denied: s.t("denied"), Expired: s.t("expired"), Withdrawn: s.t("withdrawn"),
		Active: s.t("active"), Exhausted: s.t("used up"), Revoked: s.t("revoked"), Released: s.t("released"),
		Request: s.t("Request"), ChangedAt: s.t("Asked"), Yes: s.t("yes"), No: s.t("no"),
	}
}

// secretApprovalsPage lists the waiting requests with their decision forms, and the recently
// decided ones.
func (s *Server) secretApprovalsPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := secretApprovalsPageData{shell: s.shellFor(r, v, "secrets", s.t("Secrets")+" · "+s.t("Secret approvals"),
		crumb{Label: s.t("Secrets"), Href: consolePath("/secrets")}),
		L: s.secretLabels(), CSRF: s.formSecret(w), Pending: []secrets.Request{}, Recent: []secrets.Request{}}
	for _, req := range s.Secrets.Requests("") {
		if req.Status == secrets.ReqPending {
			p.Pending = append(p.Pending, req)
		} else {
			p.Recent = append(p.Recent, req)
		}
	}
	if len(p.Recent) > 10 {
		p.Recent = p.Recent[:10]
	}
	p.Suspended = s.Secrets.SuspendedAgents()
	s.render(w, secretApprovalsTmpl, p)
}

type secretApprovalsPageData struct {
	shell
	L         secretLabels
	CSRF      string
	Pending   []secrets.Request
	Recent    []secrets.Request
	Suspended map[string]string
}

// secretDecideForm takes a console decision: the note, the uses and the duration it may set.
func (s *Server) secretDecideForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "400 bad request", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(r.PostForm.Get("note"))
	var uses, duration *int64
	if str := r.PostForm.Get("uses"); str != "" {
		if n, err := strconv.ParseInt(str, 10, 64); err == nil {
			uses = &n
		}
	}
	if str := r.PostForm.Get("duration"); str != "" {
		if n, err := strconv.ParseInt(str, 10, 64); err == nil {
			duration = &n
		}
	}
	who := secrets.DecideActor{Name: v.Principal.User, Kind: "person", Via: "console", Role: v.Account.Role}
	approve := r.PostForm.Get("decision") != "deny"
	once := r.PostForm.Get("decision") == "once"
	if once {
		one := int64(1)
		uses = &one
	}
	if _, _, err := s.Secrets.Decide(who, r.PathValue("id"), approve, note, uses, duration); err != nil {
		s.secretFail(w, r, err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/approvals"), http.StatusSeeOther)
}

// secretGrantsPage lists every grant.
func (s *Server) secretGrantsPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := secretGrantsPageData{shell: s.shellFor(r, v, "secrets", s.t("Secrets")+" · "+s.t("Grants"),
		crumb{Label: s.t("Secrets"), Href: consolePath("/secrets")}),
		L: s.secretLabels(), CSRF: s.formSecret(w), Grants: s.grantRows(s.Secrets.AllGrants(""))}
	s.render(w, secretGrantsTmpl, p)
}

// secretGrantRow is a grant as the page shows it, with the item's title.
type secretGrantRow struct {
	secrets.Grant
	ItemTitle string
}

// grantRows maps the grants to rows the templates show, resolving the item titles through the vault.
func (s *Server) grantRows(grants []secrets.Grant) []secretGrantRow {
	out := make([]secretGrantRow, 0, len(grants))
	for _, g := range grants {
		title := g.Item
		if s.Vault != nil {
			if item, _, ok := s.Vault.Item(g.Item); ok {
				title = item.Title
			}
		}
		out = append(out, secretGrantRow{Grant: g, ItemTitle: title})
	}
	return out
}

type secretGrantsPageData struct {
	shell
	L      secretLabels
	CSRF   string
	Grants []secretGrantRow
}

// secretRevokeForm ends a grant.
func (s *Server) secretRevokeForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	_ = r.ParseForm()
	who := secrets.Actor{Name: v.Principal.User, Via: "console", Role: v.Account.Role}
	if err := s.Secrets.RevokeGrant(who, r.PathValue("id"), strings.TrimSpace(r.PostForm.Get("reason"))); err != nil {
		s.secretFail(w, r, err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/grants"), http.StatusSeeOther)
}

// secretPoliciesPage shows the entitlements with their forms and the agent tags.
func (s *Server) secretPoliciesPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := secretPoliciesPageData{shell: s.shellFor(r, v, "secrets", s.t("Secrets")+" · "+s.t("Entitlements"),
		crumb{Label: s.t("Secrets"), Href: consolePath("/secrets")}),
		L: s.secretLabels(), CSRF: s.formSecret(w), Entitlements: s.Secrets.Entitlements()}
	if p.Entitlements == nil {
		p.Entitlements = []secrets.Entitlement{}
	}
	s.render(w, secretPoliciesTmpl, p)
}

type secretPoliciesPageData struct {
	shell
	L            secretLabels
	CSRF         string
	Entitlements []secrets.Entitlement
}

// csv splits a form field's comma-separated list.
func csv(form string) []string {
	var out []string
	for _, part := range strings.Split(form, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// secretEntitlementForm writes an entitlement from its form.
func (s *Server) secretEntitlementForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "400 bad request", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	e := secrets.Entitlement{
		Name:        strings.TrimSpace(f.Get("name")),
		Description: strings.TrimSpace(f.Get("description")),
		Deny:        f.Get("deny") == "yes",
		Principals: secrets.Principals{
			Agents:    csv(f.Get("agents")),
			AgentTags: csv(f.Get("agent_tags")),
			AgentsOf:  csv(f.Get("agents_of")),
		},
		Resources: secrets.Resources{
			Collections: csv(f.Get("collections")),
			Tags:        csv(f.Get("tags")),
			Items:       csv(f.Get("items")),
			Fields:      csv(f.Get("fields")),
		},
		Actions:   csv(f.Get("actions")),
		Approval:  strings.TrimSpace(f.Get("approval")),
		Reviewers: csv(f.Get("reviewers")),
		Template:  f.Get("template") == "yes",
	}
	who := secrets.Actor{Name: v.Principal.User, Via: "console", Role: v.Account.Role}
	if _, err := s.Secrets.SetEntitlement(who, e); err != nil {
		s.secretFail(w, r, err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/policies"), http.StatusSeeOther)
}

// secretEntitlementDeleteForm removes one.
func (s *Server) secretEntitlementDeleteForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	_ = r.ParseForm()
	who := secrets.Actor{Name: v.Principal.User, Via: "console", Role: v.Account.Role}
	if err := s.Secrets.DeleteEntitlement(who, strings.TrimSpace(r.PostForm.Get("name"))); err != nil {
		s.secretFail(w, r, err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/policies"), http.StatusSeeOther)
}

// secretAgentTagsForm sets an agent's grouping tags.
func (s *Server) secretAgentTagsForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	_ = r.ParseForm()
	who := secrets.Actor{Name: v.Principal.User, Via: "console", Role: v.Account.Role}
	if err := s.Secrets.SetAgentTags(who, strings.TrimSpace(r.PostForm.Get("agent")), csv(r.PostForm.Get("tags"))); err != nil {
		s.secretFail(w, r, err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/policies"), http.StatusSeeOther)
}

// agentSecretsPage is the agent page's "secret scope".
func (s *Server) agentSecretsPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	if err := store.ValidAgentName(name); err != nil {
		notFound(w)
		return
	}
	if !s.ownsOrAdminsAgent(v, name) {
		s.forbidden(w, r, v, "secrets:admin")
		return
	}
	if s.Secrets == nil {
		notFound(w)
		return
	}
	p := agentSecretsPageData{shell: s.shellFor(r, v, "agents", s.t("Secret scope")+" · "+name,
		crumb{Label: s.t("Agents"), Href: consolePath("/agents")}, crumb{Label: name, Href: consolePath("/agents/" + name)}),
		L: s.secretLabels(), CSRF: s.formSecret(w), Agent: name,
		Entitlements: s.Secrets.EntitlementsOfAgent(name), Tags: s.Secrets.TagsOfAgent(name),
		Preview: s.Secrets.PreviewForAgent(name), Grants: s.grantRows(s.Secrets.GrantsOf(name)), Templates: s.Secrets.TemplateEntitlements()}
	s.render(w, agentSecretsTmpl, p)
}

type agentSecretsPageData struct {
	shell
	L            secretLabels
	CSRF         string
	Agent        string
	Entitlements []secrets.Entitlement
	Tags         []string
	Preview      secrets.ScopePreview
	Grants       []secretGrantRow
	Templates    []secrets.Entitlement
}

// secretTemplatesForm hands an agent the template entitlements named.
func (s *Server) secretTemplatesForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	name := r.PathValue("name")
	if !s.ownsOrAdminsAgent(v, name) {
		s.forbidden(w, r, v, "secrets:admin")
		return
	}
	_ = r.ParseForm()
	if err := store.ValidAgentName(name); err != nil {
		notFound(w)
		return
	}
	who := secrets.Actor{Name: v.Principal.User, Via: "console", Role: v.Account.Role}
	if err := s.Secrets.ApplyTemplates(who, name, r.Form["template"]); err != nil {
		s.secretFail(w, r, err)
		return
	}
	back, err := url.JoinPath(consolePath("/agents/"), name, "secrets")
	if err != nil {
		s.secretFail(w, r, err)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // G710: name is a validated agent name and the path stays on the console
}

// ownsOrAdminsAgent says whether the viewer owns the agent, holds secrets:admin, or is an owner.
func (s *Server) ownsOrAdminsAgent(v *viewer, name string) bool {
	if v.hasAny("secrets:admin") {
		return true
	}
	if v.Account != nil && v.Account.Role == "owner" {
		return true
	}
	if s.Secrets == nil {
		return false
	}
	owner, ok := s.Secrets.AgentOwner(name)
	return ok && owner == v.Principal.User
}

// secretUnsuspendForm lifts a suspension.
func (s *Server) secretUnsuspendForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	_ = r.ParseForm()
	who := secrets.Actor{Name: v.Principal.User, Via: "console", Role: v.Account.Role}
	if err := s.Secrets.UnsuspendAgent(who, r.PathValue("name")); err != nil {
		s.secretFail(w, r, err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/approvals"), http.StatusSeeOther)
}

// secretFail answers a failed form the console's way.
func (s *Server) secretFail(w http.ResponseWriter, r *http.Request, err error) {
	http.Error(w, "400 "+err.Error(), http.StatusBadRequest)
}
