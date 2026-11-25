package web

import (
	"net/http"
	"net/url"
	"time"
)

var shareLinksTmpl = parse("sharelinks.html")

type shareLinkLabels struct {
	Subject, Create, Created, Expires, Copy, Copied, CopyError string
	RevokeHeading, Revoke, Back, SessionID                     string
}

type shareLinksPage struct {
	shell
	L                            shareLinkLabels
	Resource, SessionID          string
	Back, Form                   string
	Audience, Revocation         string
	IssuedURL, Expiry, ExpiryISO string
}

func (s *Server) renderShareLinks(w http.ResponseWriter, r *http.Request, v *viewer, kind, id, title, issuedURL string, expires time.Time, problem string, code int) {
	section := "/" + kind
	back := consolePath(section + "/" + url.PathEscape(id))
	labels := shareLinkLabels{
		Create: s.t("Create share link"), Created: s.t("Share link ready"), Expires: s.t("Expires"),
		Copy: s.t("Copy link"), Copied: s.t("Link copied."), CopyError: s.t("Select the link text to copy it."),
		RevokeHeading: s.t("Revoke existing links"), Revoke: s.t("Revoke all share links"),
		Subject: s.t("Share this alert"), Back: s.t("Back to alert"), SessionID: s.t("Session ID"),
	}
	sectionLabel := s.t("Alerts")
	audience := s.t("Anyone with a share link can view the incident it opens until the link expires or is revoked.")
	revocation := s.t("Earlier share links to any incident in this alert group will stop working, including links in chat.")
	var sessionID string
	if kind == "sessions" {
		sectionLabel = s.t("Sessions")
		labels.Subject, labels.Back = s.t("Share this session"), s.t("Back to session")
		audience = s.t("Anyone with a share link can view this session and download its recording until the link expires or is revoked.")
		revocation = s.t("Earlier share links to this session and its recording will stop working, including links in chat.")
		sessionID = id
	}
	p := shareLinksPage{
		shell: s.shellFor(r, v, kind, s.t("Manage share links"), crumb{Label: sectionLabel, Href: consolePath(section)}, crumb{Label: title, Href: back}),
		L:     labels, Resource: title, SessionID: sessionID, Back: back, Form: back + "/share-links",
		Audience: audience, Revocation: revocation, IssuedURL: issuedURL,
	}
	if issuedURL != "" && !expires.IsZero() {
		p.Expiry, p.ExpiryISO = expires.In(s.loc()).Format("2006-01-02 15:04:05 MST"), iso(expires)
	}
	p.Problem = problem
	s.renderStatus(w, code, shareLinksTmpl, p)
}
