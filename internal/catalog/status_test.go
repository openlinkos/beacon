package catalog

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const pages = `status_pages:
  - name: public
    title: {en: Example Status, zh: Example Status (zh)}
    description: Whether Example's public services work.
    host: uptime.example.com
    languages: [en, zh]
    components:
      - {name: api, title: API, services: [api.example.com]}
      - {name: mirrors, title: {en: Mirrors}, page: mirrors}
    groups:
      - {title: Registries, components: [mirrors]}
    links: {home: "https://example.com/", abuse: "mailto:abuse@example.com"}
    menu:
      title: Example
      groups:
        - links: [{title: Example home, url: "https://example.com/"}]
  - name: internal
    title: Internal
    default: true
    components:
      - {name: api, title: API, services: [api.example.com]}
`

func TestStatusPages(t *testing.T) {
	c, err := Parse(map[string][]byte{"pages.yaml": []byte(pages)})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := c.StatusPage("public")
	if !ok || p.Title.In("zh") != "Example Status (zh)" || p.Title.In("en") != "Example Status" || p.Description.In("zh") != "Whether Example's public services work." {
		t.Fatalf("public: %+v", p)
	}
	if m, _ := p.Component("mirrors"); m.Title.In("zh") != "Mirrors" {
		t.Errorf("a text without the language falls back to English: %q", m.Title.In("zh"))
	}
	if d, ok := c.DefaultStatusPage(); !ok || d.Name != "internal" {
		t.Errorf("default: %v", d.Name)
	}
	if h, ok := c.StatusPageByHost("Uptime.Example.com"); !ok || h.Name != "public" {
		t.Errorf("by host: %v", h.Name)
	}
	if i, _ := c.StatusPage("internal"); !slices.Equal(i.Langs(), []string{"en"}) || !slices.Equal(i.IncidentAlerts(), DefaultStatusAlerts) || i.AllNotes() {
		t.Errorf("defaults: %v %v", i.Langs(), i.IncidentAlerts())
	}

	// a text is a string or one per language, and comes back as it went in
	for _, in := range []string{`"Example"`, `{"en":"Example","zh":"Example (zh)"}`} {
		var tx Text
		if err := json.Unmarshal([]byte(in), &tx); err != nil {
			t.Fatal(err)
		}
		if out, _ := json.Marshal(tx); string(out) != in {
			t.Errorf("JSON round trip: %s -> %s", in, out)
		}
		y, _ := yaml.Marshal(tx)
		var back Text
		if err := yaml.Unmarshal(y, &back); err != nil || back.In("en") != "Example" {
			t.Errorf("YAML round trip: %s %v", y, err)
		}
	}
}

func TestStatusPageChecks(t *testing.T) {
	base := func(extra string) string {
		return "status_pages:\n  - name: p\n    title: T\n    components:\n      - {name: c, title: C, services: [s]}\n" + extra
	}
	for name, tc := range map[string]struct{ yaml, want string }{
		"name":        {"status_pages:\n  - {name: P!, title: T, components: [{name: c, title: C, services: [s]}]}\n", "lower case"},
		"title":       {"status_pages:\n  - {name: p, components: [{name: c, title: C, services: [s]}]}\n", "title is required"},
		"twice":       {base("  - {name: p, title: T, components: [{name: c, title: C, services: [s]}]}\n"), "listed twice"},
		"components":  {"status_pages:\n  - {name: p, title: T}\n", "at least one component"},
		"no services": {"status_pages:\n  - {name: p, title: T, components: [{name: c, title: C}]}\n", "names no services"},
		"query":       {"status_pages:\n  - {name: p, title: T, components: [{name: c, title: C, services: ['a\"} or vector(1)']}]}\n", "cannot name targets"},
		"group":       {base("    groups: [{title: G, components: [nope]}]\n"), "does not have"},
		"two groups":  {base("    groups: [{title: G, components: [c]}, {title: H, components: [c]}]\n"), "two groups"},
		"language":    {base("    languages: [fr]\n"), "languages are en and zh"},
		"text lang":   {"status_pages:\n  - {name: p, title: {fr: T}, components: [{name: c, title: C, services: [s]}]}\n", "is not a language"},
		"host":        {base("    host: https://status.example.com\n"), "not a host name"},
		"link":        {base("    links: {home: \"javascript:alert(1)\"}\n"), "not an http(s) or mailto link"},
		"menu link":   {base("    menu: {title: M, groups: [{links: [{title: X, url: \"data:text/html,x\"}]}]}\n"), "menu link"},
		"menu title":  {base("    menu: {groups: [{links: [{title: X, url: \"https://example.com\"}]}]}\n"), "needs a title"},
		"brand":       {base("    brand: acme\n"), "brand must be one of"},
		"alerts":      {base("    alerts: [\"Service Down\"]\n"), "not an alert name"},
		"notes":       {base("    notes: some\n"), "notes is public"},
		"same host": {"status_pages:\n  - {name: a, title: T, host: s.example.com, components: [{name: c, title: C, services: [s]}]}\n" +
			"  - {name: b, title: T, host: s.example.com, components: [{name: c, title: C, services: [s]}]}\n", "is the host of status page"},
		"defaults": {"status_pages:\n  - {name: a, title: T, default: true, components: [{name: c, title: C, services: [s]}]}\n" +
			"  - {name: b, title: T, default: true, components: [{name: c, title: C, services: [s]}]}\n", "all marked default"},
		"unknown field": {base("    colour: red\n"), "not found in type"},
	} {
		_, err := Parse(map[string][]byte{"p.yaml": []byte(tc.yaml)})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}
