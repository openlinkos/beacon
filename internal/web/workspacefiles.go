package web

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/openlinkos/beacon/internal/web/link"
)

const workspaceRoot = "static/workspace/"

func (s *Server) workspaceEntry(handler http.HandlerFunc) http.HandlerFunc {
	if !s.DefaultWorkspace || !workspaceBuilt(static) {
		return handler
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/html") || r.Header.Get("Authorization") != "" || r.URL.Query().Has(link.Param) {
			handler(w, r)
			return
		}
		headers(w)
		w.Header().Set("Cache-Control", "private, no-store")
		to := "/workspace" + strings.TrimPrefix(r.URL.RequestURI(), link.Root)
		redirectLocal(w, to, http.StatusFound)
	}
}

func workspaceBuilt(files fs.FS) bool {
	_, err := fs.Stat(files, workspaceRoot+"index.html")
	return err == nil
}

// The release build copies the independent frontend bundle into the embed FS
// before compiling Beacon. Development Go builds can run without that bundle.
func registerWorkspaceFiles(mux *http.ServeMux, files fs.FS) {
	index, err := fs.ReadFile(files, workspaceRoot+"index.html")
	if err != nil {
		return
	}
	entry := func(w http.ResponseWriter, _ *http.Request) {
		headers(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}
	for _, route := range []string{
		"/workspace/{$}", "/workspace/alerts", "/workspace/alerts/{id}", "/workspace/alerts/{id}/episodes/{episode}", "/workspace/sessions/{id}",
	} {
		mux.HandleFunc("GET "+route, entry)
	}
	mux.HandleFunc("GET /workspace/assets/{file}", func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
	assets, _ := fs.ReadDir(files, workspaceRoot+"assets")
	for _, asset := range assets {
		if asset.IsDir() {
			continue
		}
		var contentType string
		switch path.Ext(asset.Name()) {
		case ".js":
			contentType = "text/javascript; charset=utf-8"
		case ".css":
			contentType = "text/css; charset=utf-8"
		default:
			continue
		}
		// Only the files discovered in the embedded build have serving routes.
		filename := asset.Name()
		mux.HandleFunc("GET /workspace/assets/"+filename, func(w http.ResponseWriter, _ *http.Request) {
			data, err := fs.ReadFile(files, workspaceRoot+"assets/"+filename)
			if err != nil {
				notFound(w)
				return
			}
			headers(w)
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			_, _ = w.Write(data)
		})
	}
}

func registerWorkspacePageEntry(mux *http.ServeMux, route string) {
	mux.HandleFunc("GET /workspace"+route, func(w http.ResponseWriter, _ *http.Request) {
		headers(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		data, _ := static.ReadFile(workspaceRoot + "index.html")
		_, _ = w.Write(data)
	})
}
