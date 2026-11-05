package web

import (
	"net/http"

	"github.com/openlinkos/beacon/internal/secrets"
)

// The console's hygiene page: the items whose rotation period ends soon or has passed, the password
// health report (weak, repeated, old — and leaked, only when the deployment turned the leak corpus
// on, which the page says plainly, for that setting sends the first five characters of each hash
// outward), and the grants that have sat unused longer than the setting allows, marked whatever
// grant.on_unused does about them.

// hygieneUI is what the hygiene page needs of the vault, an interface so the page can be tested
// against a stub.
type hygieneUI interface {
	HealthReport(who secrets.Actor) (secrets.HealthReport, error)
	RotationDue() []secrets.RotationRow
	UnusedGrants() []secrets.Grant
}

var hygieneTmpl = parse("secretshygiene.html")

// registerHygienePage adds the hygiene page under the console.
func (s *Server) registerHygienePage(at func(method, path string, h http.HandlerFunc)) {
	at("GET", "/secrets/hygiene", s.console("", s.hygienePage))
}

// hygieneLabels are the page's own strings, rendered in the console's language.
type hygieneLabels struct {
	Hygiene, HygieneHint, Rotation, RotationHint, Item, Collection, State, Due, DaysLeft,
	Overdue, DueSoon, Health, HealthHint, Field, Findings, Weak, Repeated, Old, Leaked,
	Shares, Age, HIBPOn, HIBPOnHint, HIBPOff, HIBPOffHint, Unused, UnusedHint, Idle, Agent,
	Status, None string
}

func (s *Server) hygieneLabels() hygieneLabels {
	return hygieneLabels{
		Hygiene: s.t("Hygiene"), HygieneHint: s.t("Rotation, password health and the grants nobody has used."),
		Rotation: s.t("Rotation due"), RotationHint: s.t("Items whose rotation period ends inside the notice window or has passed; the sweep tells the chat and the agents holding a grant."),
		Item: s.t("Item"), Collection: s.t("Collection"), State: s.t("State"), Due: s.t("Due"), DaysLeft: s.t("Days left"),
		Overdue: s.t("overdue"), DueSoon: s.t("due soon"),
		Health: s.t("Password health"), HealthHint: s.t("Weak, repeated and old passwords, computed locally; references only, never values."),
		Field: s.t("Field"), Findings: s.t("Findings"), Weak: s.t("weak"), Repeated: s.t("repeated"), Old: s.t("old"), Leaked: s.t("in the leak corpus"),
		Shares: s.t("Worn by"), Age: s.t("Age (days)"),
		HIBPOn: s.t("The leak corpus is being asked"), HIBPOnHint: s.t("hygiene.hibp_check is on: each password's hash prefix (five characters) is sent to the k-anonymity interface to learn whether the value is in a known leak. Turn the setting off to keep everything local."),
		HIBPOff: s.t("The leak corpus is not being asked"), HIBPOffHint: s.t("hygiene.hibp_check is off (the default): nothing leaves this server. Turn it on in the settings to check passwords against known leaks; doing so sends each password's hash prefix (five characters) outward."),
		Unused: s.t("Unused grants"), UnusedHint: s.t("Live grants that have sat idle longer than the setting allows; what happens to them is grant.on_unused (marked, the owner told, or released)."),
		Idle: s.t("Idle since"), Agent: s.t("Agent"), Status: s.t("Status"), None: s.t("None"),
	}
}

// hygienePage shows the three reports on one page.
func (s *Server) hygienePage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := hygienePageData{shell: s.shellFor(r, v, "secrets", s.t("Secrets")+" · "+s.t("Hygiene"),
		crumb{Label: s.t("Secrets"), Href: consolePath("/secrets")}), L: s.hygieneLabels(),
		Rotation: []secrets.RotationRow{}, Findings: []secrets.HealthFinding{}, Unused: []secretGrantRow{}}
	if s.Hygiene != nil {
		who, _ := vaultActorOf(v)
		if health, err := s.Hygiene.HealthReport(who); err == nil {
			p.Findings = health.Findings
			p.HIBP, p.HIBPFailed = health.HIBP, health.HIBPFailed
		}
		p.Rotation = s.Hygiene.RotationDue()
		if p.Rotation == nil {
			p.Rotation = []secrets.RotationRow{}
		}
		p.Unused = s.grantRows(s.Hygiene.UnusedGrants())
	}
	s.render(w, hygieneTmpl, p)
}

type hygienePageData struct {
	shell
	L          hygieneLabels
	Rotation   []secrets.RotationRow
	Findings   []secrets.HealthFinding
	HIBP       bool
	HIBPFailed int
	Unused     []secretGrantRow
}
