package web

import (
	"html/template"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/version"
)

// Beacon is open source, and every page that runs on it says so and links to the project: the public
// pages in their footer ("Powered by Beacon, open source", which a deployment may leave out with
// branding.powered_by: false), the console at the foot of every page with its version and where to
// read, see the code and report an issue, and the sign-in page under its form.

const (
	// siteURL is the project's site, with its documentation; the Chinese pages are under zh/.
	siteURL = "https://beacon.openlinkos.com/"
	// issuesURL is where to report an issue, through the repository's templates.
	issuesURL = version.Repo + "/issues/new/choose"
)

// docsURL is the documentation's root in the language l.
func docsURL(l i18n.Lang) string {
	if l == i18n.ZH {
		return siteURL + "zh/"
	}
	return siteURL
}

// versionText is a version as people read it: v0.3.0, or dev for a build of no release.
func versionText(v string) string {
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

// project is what a console page says about Beacon: which version, and where the project is.
type project struct {
	Version, Docs, Repo, Issues                   string
	DocsLabel, RepoLabel, IssuesLabel, OpenSource string
}

func (s *Server) project() project {
	return project{Version: "Beacon " + versionText(s.version()), Docs: docsURL(s.Lang), Repo: version.Repo, Issues: issuesURL,
		DocsLabel: s.t("Documentation"), RepoLabel: s.t("GitHub"), IssuesLabel: s.t("Report an issue"), OpenSource: s.t("Beacon is open source.")}
}

// powered is the public pages' line about Beacon, in the page's language.
type powered struct {
	Mark                         template.HTML
	Label, Docs, Repo, RepoLabel string
}

// poweredBy is that line in l, or nil when the configuration leaves it out.
func (s *Server) poweredBy(l i18n.Lang) *powered {
	if !s.Branding.Powered() {
		return nil
	}
	return &powered{Mark: template.HTML(beaconMark), Label: i18n.T(l, "Powered by Beacon, open source"), //nolint:gosec // G203: constant markup
		Docs: docsURL(l), Repo: version.Repo, RepoLabel: i18n.T(l, "GitHub")}
}
