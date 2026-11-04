package configdb

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

const pageJSON = `{"title": {"en": "Example Status", "zh": "Example Status (zh)"}, "languages": ["en", "zh"],
  "components": [{"name": "web", "title": "Web", "services": ["linkos.org"]}, {"name": "nodes", "title": "Nodes", "page": "nodes"}]}`

func TestStatusPageItems(t *testing.T) {
	e := imported(t)
	ch, err := e.s.Put(ctx, api, "status_pages", "public", json.RawMessage(pageJSON), "the public page")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Items) != 1 || ch.Items[0].Kind != KindStatus || ch.Items[0].Key != "public" || ch.Items[0].Op != "added" {
		t.Fatalf("put: %+v", ch)
	}
	if !strings.Contains(e.bot.last(), "added status page public via API") {
		t.Errorf("echo: %q", e.bot.last())
	}
	// the page label names no target yet: a warning, not a refusal
	if len(ch.Warnings) != 1 || !strings.Contains(ch.Warnings[0], `page label "nodes"`) {
		t.Errorf("warnings: %v", ch.Warnings)
	}
	v, err := e.s.Get("status_pages", "public")
	if err != nil || len(v.StatusPages) != 1 || v.StatusPages[0].Name != "public" || v.StatusPages[0].Title.In("zh") != "Example Status (zh)" {
		t.Fatalf("get: %+v %v", v, err)
	}
	if all, _ := e.s.Get("", ""); len(all.StatusPages) != 1 {
		t.Errorf("everything holds the page: %+v", all.StatusPages)
	}
	if !strings.Contains(e.s.Summary(), ", 1 status pages") {
		t.Errorf("summary: %s", e.s.Summary())
	}

	// a target labelled with the page backs the component, unless it is internal
	_, err = e.s.Put(ctx, api, "targets", "all-nodes", json.RawMessage(`[
		{"targets": ["https://n1.example/"], "labels": {"module": "http_head", "service": "n1", "page": "nodes"}},
		{"targets": ["https://n2.example/"], "labels": {"module": "http_head", "service": "n2", "page": "nodes", "visibility": "internal"}}]`), "")
	if err != nil {
		t.Fatal(err)
	}
	st := e.s.State()
	p, _ := st.Catalog.StatusPage("public")
	if got := st.PageServices(p.Components[1]); !slices.Equal(got, []string{"n1"}) {
		t.Errorf("page services: %v", got)
	}
	if w := e.s.Warnings(); len(w) != 0 {
		t.Errorf("no warnings once targets back every component: %v", w)
	}

	// refused: a broken page, a name that differs from the path, unknown fields
	for name, tc := range map[string]struct {
		value string
		want  error
	}{
		"no components": {`{"title": "T"}`, ErrInvalid},
		"other name":    {`{"name": "other", "title": "T", "components": [{"name": "c", "title": "C", "services": ["s"]}]}`, ErrBadInput},
		"unknown":       {`{"title": "T", "colour": "red", "components": [{"name": "c", "title": "C", "services": ["s"]}]}`, ErrBadInput},
		"bad link":      {`{"title": "T", "links": {"home": "javascript:x"}, "components": [{"name": "c", "title": "C", "services": ["s"]}]}`, ErrInvalid},
	} {
		if _, err := e.s.Put(ctx, api, "status_pages", "public", json.RawMessage(tc.value), ""); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// exported with the catalogue, and imported back unchanged
	files, _, err := e.s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files["catalog/status_pages.yaml"], "name: public") || !strings.Contains(files["catalog/status_pages.yaml"], "zh: Example Status (zh)") {
		t.Errorf("export:\n%s", files["catalog/status_pages.yaml"])
	}
	if again, err := e.s.Import(ctx, api, files, "", true); err != nil || !again.Unchanged {
		t.Errorf("re-import: %+v %v", again, err)
	}

	// removed, and the removal reverted
	del, err := e.s.Delete(ctx, api, "status_page", "public", "")
	if err != nil || len(e.s.Catalog().StatusPages) != 0 {
		t.Fatalf("delete: %v", err)
	}
	if _, err := e.s.Revert(ctx, api, del.Version, ""); err != nil || len(e.s.Catalog().StatusPages) != 1 {
		t.Errorf("revert: %v", err)
	}
	if _, err := e.s.Get("pages", ""); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "status_pages") {
		t.Errorf("an unknown kind names the kinds: %v", err)
	}
}
