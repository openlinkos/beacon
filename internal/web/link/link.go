// Package link signs requests for the alert and session pages people open from chat.
// Beacon stores the signing key and a revocation generation for each alert group or session.
// The method, exact path, expiry and generation are authenticated by HMAC-SHA256.
package link

import (
	"net/url"
	"regexp"
	"strings"
)

// Pages builds the links to Beacon's pages: absolute under PublicURL and signed. A nil Pages, one
// without a signer or without a public URL builds none, so that callers can leave links out.
type Pages struct {
	Signer    *Signer
	PublicURL string
}

// idRe is what an id in a page path may look like; anything else gets no link.
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var episodeRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ValidEpisode reports whether episode identifies an individual incident.
func ValidEpisode(episode string) bool { return episodeRe.MatchString(episode) }

// URL is the signed link to the page at path (which begins with "/"), or "".
func (p *Pages) URL(path string) string {
	if p == nil || p.Signer == nil || p.PublicURL == "" || !strings.HasPrefix(path, "/") {
		return ""
	}
	query := p.Signer.Query(path)
	if query == "" {
		return ""
	}
	return strings.TrimSuffix(p.PublicURL, "/") + path + "?" + query
}

// Alert is the link to an alert group's page.
func (p *Pages) Alert(id string) string { return p.Incident(id, "") }

// Incident is the signed link to an episode. Older groups without an episode use their group page.
func (p *Pages) Incident(id, episode string) string {
	if !idRe.MatchString(id) || (episode != "" && !ValidEpisode(episode)) {
		return ""
	}
	return p.URL(IncidentPath(id, episode))
}

// Session is the link to a session's page, with its replay.
func (p *Pages) Session(id string) string {
	if !idRe.MatchString(id) {
		return ""
	}
	return p.URL(SessionPath(id))
}

// Root is where the console is, with the pages a link opens. Until 0.3.0 they were at the site's root,
// and the links handed out until then name those paths: Moved says which of them a link may still open.
const Root = "/console"

// SignInPath opens a local page after the recipient signs in.
func SignInPath(next string) string { return Root + "/sign-in?next=" + url.QueryEscape(next) }

// AlertPath is the path of an alert group's page.
func AlertPath(id string) string { return Root + "/alerts/" + id }

// IncidentPath pins an alert link to one episode of its recurring group.
func IncidentPath(id, episode string) string {
	if episode == "" {
		return AlertPath(id)
	}
	return AlertPath(id) + "/episodes/" + episode
}

// SessionPath is the path of a session's page.
func SessionPath(id string) string { return Root + "/sessions/" + id }

// RecordingPath is the path of a session's recording, which its page plays.
func RecordingPath(id string) string { return Root + "/sessions/" + id + "/recording" }

// Moved maps the path of a page as the links made before the console moved under Root named it
// (/alerts/{id}, /sessions/{id} and its recording) to the path the page has now; ok is false for any
// other path, which no link ever named.
func Moved(old string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(old, "/"), "/")
	if len(parts) < 2 || !idRe.MatchString(parts[1]) {
		return "", false
	}
	switch {
	case len(parts) == 2 && parts[0] == "alerts":
		return AlertPath(parts[1]), true
	case len(parts) == 2 && parts[0] == "sessions":
		return SessionPath(parts[1]), true
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "recording":
		return RecordingPath(parts[1]), true
	}
	return "", false
}
