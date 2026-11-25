package web

import (
	"net/http"
	"strings"
)

const workspacePageRoot = "/console/api/v1/page"

// These page resources use the same role checks, validation, audit writes and
// concurrency guards as the corresponding console routes.
func workspacePageRoute(route string) bool {
	if route == "/alerts/{id}/silence" {
		return true
	}
	switch strings.SplitN(strings.TrimPrefix(route, "/"), "/", 2)[0] {
	case "sign-in", "sign-out", "setup", "join", "reset", "account", "people", "agents",
		"machines", "monitors", "sessions", "status-pages", "maintenance", "silences",
		"tokens", "config", "credentials", "hooks", "audit", "changes",
		"incident-outcomes", "incident-episodes":
		return true
	}
	return strings.HasSuffix(route, "/share-links")
}

func workspacePage(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		w.Header().Set("Cache-Control", "private, no-store")
		if !sameOrigin(r) {
			workspaceError(w, http.StatusForbidden, "auth.forbidden", "the request must be same-origin")
			return
		}

		original := r.Clone(r.Context())
		original.URL.Path = consolePath(strings.TrimPrefix(r.URL.Path, workspacePageRoot))
		if r.URL.RawPath != "" {
			original.URL.RawPath = consolePath(strings.TrimPrefix(r.URL.RawPath, workspacePageRoot))
		}
		original.RequestURI = original.URL.RequestURI()
		handler(&workspacePageWriter{ResponseWriter: w}, original)
	}
}

type workspacePageWriter struct {
	http.ResponseWriter
	sent bool
}

func (w *workspacePageWriter) page(status int, data any) {
	var header shell
	if p, ok := data.(interface{ workspaceHeader() shell }); ok {
		header = p.workspaceHeader()
	}
	kind := "page"
	switch data.(type) {
	case oncePage:
		kind = "once"
	case messagePage:
		kind = "message"
	}
	w.sent = true
	// Form validation can return a document with a 4xx status and its field data.
	workspaceJSON(w.ResponseWriter, status, struct {
		Kind, Heading, Count, Problem, Toast, ToastTone, Lang string
		Badges                                                []badge
		Crumbs                                                []crumb
		Data                                                  any
	}{kind, header.Heading, header.Count, header.Problem, header.Toast, header.ToastTone,
		header.Lang, header.Badges, header.Crumbs, data})
}

func (w *workspacePageWriter) WriteHeader(status int) {
	if w.sent {
		return
	}
	w.sent = true
	if redirect := w.Header().Get("Location"); status >= 300 && status < 400 && redirect != "" {
		w.Header().Del("Location")
		workspaceJSON(w.ResponseWriter, http.StatusOK, struct{ Redirect string }{local(redirect)})
		return
	}
	workspaceError(w.ResponseWriter, status, "page.unavailable", http.StatusText(status))
}

func (w *workspacePageWriter) Write(data []byte) (int, error) {
	if !w.sent {
		w.WriteHeader(http.StatusInternalServerError)
	}
	// http.Redirect and http.Error also write an HTML body after the status.
	return len(data), nil
}

func (s shell) workspaceHeader() shell { return s }
