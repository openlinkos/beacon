package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShareLinkViewPresentsExplicitIssueAndResourceRevocation(t *testing.T) {
	e := newStatusEnv(t)
	const issued = "https://beacon.example.com/console/sessions/example?s=expiry.signature"
	cookie := e.signedInCookie()["Cookie"]
	render := func(kind, url string, expiry time.Time) string {
		t.Helper()
		path := "/console/" + kind + "/example/share-links"
		r := httptest.NewRequest(http.MethodGet, path+"?s=unexpected", nil)
		r.Header.Set("Cookie", cookie)
		v := e.srv.viewer(r)
		if v == nil {
			t.Fatal("missing fixture viewer")
		}
		w := httptest.NewRecorder()
		e.srv.renderShareLinks(w, r, v, kind, "example", "Example resource", url, expiry, "", http.StatusOK)
		body := w.Body.String()
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(body, "unexpected") ||
			!strings.Contains(body, `action="`+path+`"`) || !strings.Contains(body, `name="csrf" value="`+v.Session.CSRF+`"`) {
			t.Fatalf("management view form or privacy headers: %d", w.Code)
		}
		return body
	}
	alert := render("alerts", "", time.Time{})
	if strings.Contains(alert, "issued-share-url") || !strings.Contains(alert, `value="create"`) ||
		!strings.Contains(alert, "any incident in this alert group") || !strings.Contains(alert, `aria-describedby="share-revocation-effect"`) {
		t.Fatal("the alert management view did not show the group-wide effect and explicit create button")
	}
	session := render("sessions", issued, e.now.Add(time.Hour))
	if !strings.Contains(session, "this session and its recording will stop working") || strings.Count(session, issued) != 1 ||
		!strings.Contains(session, `<code id="issued-share-url">`+issued+`</code>`) ||
		!strings.Contains(session, `data-copy-source="issued-share-url"`) || !strings.Contains(session, `id="share-link-heading" tabindex="-1" autofocus`) {
		t.Fatal("the issued link was not available as selectable text or the session's scope was missing")
	}
}
