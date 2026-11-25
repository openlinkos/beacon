package configdb

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/openlinkos/beacon/internal/catalog"
)

const componentEditorPage = `{
	"title":{"en":"Example Status","zh":"Example Status in Chinese"},
	"description":{"en":"Overview","zh":"Overview in Chinese"},
	"default":true,"host":"status.example.com","brand":"beacon","languages":["zh","en"],
	"notes":"all","alerts":["ServiceDown","OtherAlert"],
	"components":[
		{"name":"web","title":{"en":"Website","zh":"Website in Chinese"},"description":{"en":"Web description"},"page":"nodes","services":["legacy","linkos.org"]},
		{"name":"api","title":"API","services":["cn"]},
		{"name":"mirrors","title":"Mirrors","page":"nodes"}
	],
	"groups":[
		{"title":{"en":"Core","zh":"Core in Chinese"},"description":{"en":"Core group"},"components":["web","api"]},
		{"title":"Registries","components":["mirrors"]}
	],
	"links":{"home":"https://example.com/","privacy":"https://example.com/privacy"},
	"menu":{"title":{"en":"Navigation","zh":"Navigation in Chinese"},"groups":[
		{"title":{"en":"Organisation"},"links":[{"title":{"en":"Home","zh":"Home in Chinese"},"url":"https://example.com/"}]}
	]}
}`

func statusComponentState(t *testing.T) *env {
	t.Helper()
	e := imported(t)
	if _, err := e.s.Put(ctx, api, KindStatus, "public", json.RawMessage(componentEditorPage), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Put(ctx, api, KindTargets, "all-nodes", json.RawMessage(`[
		{"targets":["https://node.example/"],"labels":{"module":"http_head","service":"derived","page":"nodes"}},
		{"targets":["https://private.example/"],"labels":{"module":"http_head","service":"internal","page":"nodes","visibility":"internal"}}
	]`), ""); err != nil {
		t.Fatal(err)
	}
	return e
}

func statusDraftJSON(t *testing.T, p catalog.StatusPage) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestStatusComponentDraftPreservesSnapshotAndMetadata(t *testing.T) {
	e := statusComponentState(t)
	st := e.s.State()
	original, _ := st.Catalog.StatusPage("public")
	before := statusDraftJSON(t, original)
	services := []string{"legacy", "internal", "linkos.org"}
	draft, err := st.EditStatusComponent("public", StatusComponentEdit{Component: "web", Services: services})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.PageServices(draft.Components[0]); !slices.Equal(got, []string{"derived", "internal", "legacy", "linkos.org"}) {
		t.Fatalf("explicit and label-derived service bindings: %v", got)
	}
	services[0] = "caller-modified"
	if !slices.Equal(draft.Components[0].Services, []string{"legacy", "internal", "linkos.org"}) {
		t.Error("the draft shares the caller's services slice")
	}

	// Restoring the one edited field should restore every original definition.
	draft.Components[0].Services = []string{"legacy", "linkos.org"}
	if got := statusDraftJSON(t, draft); !bytes.Equal(got, before) {
		t.Fatalf("the status draft changed more than the explicit services:\ngot  %s\nwant %s", got, before)
	}
	draft.Title["en"], draft.Description["en"] = "Changed title", "Changed description"
	draft.Languages[0], draft.Alerts[0] = "en", "AlteredAlert"
	draft.Components[0].Title["en"], draft.Components[0].Description["en"] = "Changed component", "Changed description"
	draft.Components[0].Page = "changed-selector"
	draft.Components[1].Services[0] = "changed-neighbour"
	draft.Groups[0].Title["en"], draft.Groups[0].Description["en"], draft.Groups[0].Components[0] = "Changed group", "Changed description", "changed-reference"
	draft.Menu.Title["en"], draft.Menu.Groups[0].Title["en"] = "Changed menu", "Changed group"
	draft.Menu.Groups[0].Links[0].Title["en"], draft.Menu.Groups[0].Links[0].URL = "Changed link", "https://different.example/"
	fromSnapshot, _ := st.Catalog.StatusPage("public")
	if got := statusDraftJSON(t, fromSnapshot); !bytes.Equal(got, before) {
		t.Fatalf("mutating a draft affected the snapshot:\ngot  %s\nwant %s", got, before)
	}
	if e.s.State() != st || e.s.Version() != st.Version {
		t.Fatal("drafting published a new snapshot")
	}
}

func TestStatusComponentDraftAddsAndRemovesOnlyRequestedComponent(t *testing.T) {
	st := statusComponentState(t).s.State()
	add, err := st.EditStatusComponent("public", StatusComponentEdit{Component: "queue", Title: "  Job queues  ", Services: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := add.Component("queue")
	if !ok || c.Title.In("zh") != "Job queues" || c.Page != "" || !slices.Equal(c.Services, []string{"x"}) ||
		len(add.Components) != 4 || add.Components[3].Name != "queue" {
		t.Fatalf("added component: %+v", c)
	}
	original, _ := st.Catalog.StatusPage("public")
	add.Components = add.Components[:3]
	if !bytes.Equal(statusDraftJSON(t, add), statusDraftJSON(t, original)) {
		t.Error("the add altered other definitions")
	}

	remove, err := st.EditStatusComponent("public", StatusComponentEdit{Component: "web", Remove: true, Title: "ignored", Services: []string{"unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(remove.Components) != 2 || remove.Components[0].Name != "api" || remove.Components[1].Name != "mirrors" ||
		len(remove.Groups) != 2 || !slices.Equal(remove.Groups[0].Components, []string{"api"}) ||
		remove.Groups[0].Description.In("en") != "Core group" || !slices.Equal(remove.Groups[1].Components, []string{"mirrors"}) {
		t.Fatalf("remove altered component order, unaffected groups or group metadata: %+v", remove)
	}
	emptyGroup, err := st.EditStatusComponent("public", StatusComponentEdit{Component: "mirrors", Remove: true})
	if err != nil || len(emptyGroup.Groups) != 2 || emptyGroup.Groups[1].Title.In("en") != "Registries" || len(emptyGroup.Groups[1].Components) != 0 {
		t.Fatalf("remove did not preserve the heading and clear its last reference: %+v, %v", emptyGroup.Groups, err)
	}

	labelOnly, err := st.EditStatusComponent("public", StatusComponentEdit{Component: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := labelOnly.Component("web"); len(c.Services) != 0 || !slices.Equal(st.PageServices(c), []string{"derived"}) {
		t.Fatalf("clearing explicit bindings affected the label selector: %+v", c)
	}
}

func TestStatusComponentDraftRejectsInvalidSelection(t *testing.T) {
	e := statusComponentState(t)
	if _, err := e.s.Put(ctx, api, KindStatus, "solo", json.RawMessage(`{"title":"Solo","components":[{"name":"web","title":"Website","services":["linkos.org"]}]}`), ""); err != nil {
		t.Fatal(err)
	}
	st := e.s.State()
	for _, tc := range []struct {
		name, page string
		edit       StatusComponentEdit
		want       error
	}{
		{"unknown page", "missing", StatusComponentEdit{Component: "web"}, ErrNotFound},
		{"empty component", "public", StatusComponentEdit{Title: "New", Services: []string{"x"}}, ErrBadInput},
		{"invalid component", "public", StatusComponentEdit{Component: "Other/name", Title: "Bad", Services: []string{"x"}}, ErrBadInput},
		{"unknown edit", "public", StatusComponentEdit{Component: "missing", Services: []string{"x"}}, ErrNotFound},
		{"unknown removal", "public", StatusComponentEdit{Component: "missing", Remove: true}, ErrNotFound},
		{"duplicate add", "public", StatusComponentEdit{Component: "web", Title: "Replacement", Services: []string{"x"}}, ErrBadInput},
		{"blank add title", "public", StatusComponentEdit{Component: "new", Title: "  ", Services: []string{"x"}}, ErrBadInput},
		{"long add title", "public", StatusComponentEdit{Component: "new", Title: string(bytes.Repeat([]byte{'x'}, 121)), Services: []string{"x"}}, ErrBadInput},
		{"no services on add", "public", StatusComponentEdit{Component: "new", Title: "New"}, ErrBadInput},
		{"no services on edit without a selector", "public", StatusComponentEdit{Component: "api"}, ErrBadInput},
		{"duplicate selection", "public", StatusComponentEdit{Component: "api", Services: []string{"x", "x"}}, ErrBadInput},
		{"retired service cannot be added elsewhere", "public", StatusComponentEdit{Component: "api", Services: []string{"legacy"}}, ErrBadInput},
		{"unknown service", "public", StatusComponentEdit{Component: "api", Services: []string{"never-monitored"}}, ErrBadInput},
		{"cannot remove last component", "solo", StatusComponentEdit{Component: "web", Remove: true}, ErrBadInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := st.EditStatusComponent(tc.page, tc.edit); !errors.Is(err, tc.want) {
				t.Fatalf("draft error: got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestStatusComponentDraftRequiresCurrentDatabaseVersion(t *testing.T) {
	e := imported(t)
	const publicPage = `{"title":"Example Status","components":[{"name":"web","title":"Website","services":["linkos.org"]}]}`
	const otherPage = `{"title":"Other Status","components":[{"name":"main","title":"Main","services":["linkos.org"]}]}`
	for _, fixture := range []struct{ name, value string }{{"public", publicPage}, {"other", otherPage}} {
		if _, err := e.s.Put(ctx, api, KindStatus, fixture.name, json.RawMessage(fixture.value), ""); err != nil {
			t.Fatal(err)
		}
	}
	second := Open(Deps{Store: e.st})
	snapshot := second.State()
	draft, err := snapshot.EditStatusComponent("public", StatusComponentEdit{Component: "web", Services: []string{"linkos.org", "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Delete(ctx, api, KindTargets, "all-web-x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Put(ctx, api, KindStatus, "other", json.RawMessage(`{"title":"Changed concurrently","components":[{"name":"main","title":"Main","services":["linkos.org"]}]}`), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := second.PutIf(ctx, api, KindStatus, "public", statusDraftJSON(t, draft), "", snapshot.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale status draft did not conflict when a selected target was removed: %v", err)
	}
	if second.Version() != e.s.Version() {
		t.Fatalf("stale config did not refresh after conflict: got version %d, want %d", second.Version(), e.s.Version())
	}
	page, _ := second.Catalog().StatusPage("public")
	if !slices.Equal(page.Components[0].Services, []string{"linkos.org"}) {
		t.Fatalf("the stale draft was published: %+v", page.Components)
	}
	if _, err := second.State().EditStatusComponent("public", StatusComponentEdit{Component: "web", Services: []string{"x"}}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("the reloaded snapshot still selected a removed target: %v", err)
	}
	fresh := second.State()
	draft, err = fresh.EditStatusComponent("public", StatusComponentEdit{Component: "web", Services: []string{"cn"}})
	if err != nil {
		t.Fatal(err)
	}
	change, err := second.PutIf(ctx, api, KindStatus, "public", statusDraftJSON(t, draft), "", fresh.Version)
	if err != nil || change.Version != fresh.Version+1 || len(change.Items) != 1 || change.Items[0].Key != "public" {
		t.Fatalf("the fresh draft was not published independently: %+v, %v", change, err)
	}
	reopened := Open(Deps{Store: e.st})
	other, _ := reopened.Catalog().StatusPage("other")
	if other.Title.In("en") != "Changed concurrently" {
		t.Fatalf("saving the fresh draft replaced a different status page: %+v", other)
	}
	if len(reopened.Targets().Groups("all-web-x.yaml")) != 0 {
		t.Fatal("saving the fresh draft restored a concurrently removed target")
	}
}
