package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/store"
)

func TestPublicComponentReviewsExistingIncidentNotes(t *testing.T) {
	for _, policy := range []string{"public", "all"} {
		t.Run(policy, func(t *testing.T) {
			e := newEnv(t, i18n.EN)
			e.srv.Status = status.New(status.Deps{Config: e.cdb, Store: e.st, Now: func() time.Time { return e.now }, PublicURL: publicURL})
			e.mux = http.NewServeMux()
			e.srv.Register(e.mux)
			owner := e.owner()
			put := func(kind, key, raw string) {
				t.Helper()
				if _, err := e.cdb.Put(context.Background(), configdb.Actor{Name: "fixture", Via: "cli"}, kind, key, json.RawMessage(raw), ""); err != nil {
					t.Fatal(err)
				}
			}
			put(configdb.KindModule, "http_2xx", `{"prober":"http"}`)
			put(configdb.KindTargets, "all-publication-targets", `[
				{"targets":["https://public.example.com"],"labels":{"service":"public-app","module":"http_2xx"}},
				{"targets":["https://internal.example.com"],"labels":{"service":"internal-app","module":"http_2xx"}}
			]`)
			put(configdb.KindStatus, "public", `{"title":"Fixture Status","notes":"`+policy+`","components":[{"name":"site","title":"Site","services":["public-app"]}]}`)
			const (
				publicPath  = "/status/public"
				formPath    = "/console/status-pages/public/components/add"
				publicText  = "Fixture <traffic> restored"
				privateText = "Fixture internal incident note"
				alreadyLive = "Fixture public note from a shared incident"
				newText     = "Fixture note added during review"
			)
			in := store.Incident{ID: "a1", AlertName: "ServiceDown", Status: "resolved", Services: []string{"internal-app"},
				StartsAt: e.now.Add(-time.Hour), ResolvedAt: e.now, Notes: []store.IncidentNote{{At: e.now, Text: " PUBLIC: " + publicText}, {At: e.now, Text: privateText}}}
			overlap := in
			overlap.ID, overlap.Services = "a2", []string{"public-app", "internal-app"}
			overlap.Notes = []store.IncidentNote{{At: e.now, Text: "public: " + alreadyLive}}
			write := func(incidents ...store.Incident) {
				t.Helper()
				if _, err := e.st.PutIncidents(incidents, e.now.Add(-store.KeepStatus)); err != nil {
					t.Fatal(err)
				}
			}
			write(in, overlap)
			checkAbsent := func() {
				t.Helper()
				if code, body, _ := e.get(publicPath+".json", ""); code != http.StatusOK || !strings.Contains(body, alreadyLive) || strings.Contains(body, `"id":"a1"`) {
					t.Fatalf("signed-out status changed before publication: %d %s", code, body)
				}
				if code, _, _ := e.get(publicPath+"/incidents/a1", ""); code != http.StatusNotFound {
					t.Fatalf("incident visible before publication: %d", code)
				}
			}
			checkAbsent()
			version := e.cdb.Version()
			fields := url.Values{"component": {"api"}, "title": {"API"}, "service": {"internal-app"}, "intent": {"preview"},
				"version": {strconv.FormatInt(version, 10)}}
			p := owner.post(formPath, fields)
			confirmation := regexp.MustCompile(`name="confirmation" value="([0-9a-f]{64})"`)
			match := confirmation.FindStringSubmatch(p.body)
			if p.code != http.StatusOK || len(match) != 2 || !strings.Contains(p.body, "Incident updates that will become public") ||
				!strings.Contains(p.body, "Fixture &lt;traffic&gt; restored") || strings.Contains(p.body, alreadyLive) ||
				strings.Contains(p.body, privateText) != (policy == "all") {
				t.Fatalf("incorrect preview with notes=%s: %d %s", policy, p.code, p.body)
			}
			fields.Set("intent", "publish")
			if a := owner.post(formPath, fields); a.code != http.StatusConflict || e.cdb.Version() != version {
				t.Fatalf("unreviewed notes were published: %d", a.code)
			}
			fields.Set("confirmation", match[1])
			in.Notes = append(in.Notes, store.IncidentNote{At: e.now, Text: "public: " + newText})
			write(in)
			p = owner.post(formPath, fields)
			match = confirmation.FindStringSubmatch(p.body)
			if p.code != http.StatusConflict || len(match) != 2 || !strings.Contains(p.body, newText) || e.cdb.Version() != version {
				t.Fatalf("a changed public note failed to invalidate the preview: %d", p.code)
			}
			checkAbsent()
			fields.Set("confirmation", match[1])
			if a := owner.post(formPath, fields); a.code != http.StatusSeeOther || e.cdb.Version() != version+1 {
				t.Fatalf("reviewed publication failed: %d", a.code)
			}
			if code, body, _ := e.get(publicPath+"/incidents/a1", ""); code != http.StatusOK ||
				!strings.Contains(body, "Fixture &lt;traffic&gt; restored") || !strings.Contains(body, newText) || strings.Contains(body, privateText) != (policy == "all") {
				t.Fatalf("public incident differed from its preview: %d %s", code, body)
			}
		})
	}
}
