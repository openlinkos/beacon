package targets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

var locations = []string{"monitor-us1", "cdn-pre-us1", "cdn-pro-cn1"}

func TestForPicksFilesByName(t *testing.T) {
	d := t.TempDir()
	write(t, d, "all-web.yaml", "- targets: [https://linkos.org/]\n  labels: {module: http_head, service: linkos.org}\n")
	write(t, d, "overseas-cdn-nodes.yaml", "- targets: ['203.0.113.1:443']\n  labels: {module: tcp_443, service: node-a}\n")
	write(t, d, "cdn-pro-cn1-local.yaml", "- targets: [https://example.cn/]\n  labels: {module: http_head, service: cn-only}\n")
	write(t, d, "notes.txt", "ignored")
	s, err := Load(d, locations)
	if err != nil {
		t.Fatal(err)
	}
	if s.Count() != 3 || len(s.Files()) != 3 {
		t.Fatalf("loaded %v", s.Files())
	}
	svc := func(gs []Group) string {
		var out []string
		for _, g := range gs {
			out = append(out, g.Labels["service"])
		}
		return strings.Join(out, ",")
	}
	if got := svc(s.For("cdn-pre-us1", "overseas", locations)); got != "linkos.org,node-a" {
		t.Errorf("overseas location gets %q", got)
	}
	if got := svc(s.For("cdn-pro-cn1", "mainland", locations)); got != "linkos.org,cn-only" {
		t.Errorf("mainland location gets %q", got)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]string{
		"us-cdn-nodes.yaml":  "- targets: [a]\n  labels: {module: m, service: s}\n", // no such prefix any more
		"all-nomodule.yaml":  "- targets: [a]\n  labels: {service: s}\n",
		"all-notargets.yaml": "- targets: []\n  labels: {module: m, service: s}\n",
		"all-unknown.yaml":   "- targets: [a]\n  labels: {module: m, service: s}\n  extra: 1\n",
	}
	for name, body := range cases {
		d := t.TempDir()
		write(t, d, name, body)
		if _, err := Load(d, locations); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if s, err := Load(filepath.Join(t.TempDir(), "nope"), locations); err != nil || s.Count() != 0 {
		t.Errorf("a missing directory is an empty set: %v", err)
	}
}

func TestLongestLocationPrefixWins(t *testing.T) {
	if got := scope("cdn-pre-us1-x.yaml", []string{"cdn-pre", "cdn-pre-us1"}); got != "cdn-pre-us1" {
		t.Errorf("scope = %q", got)
	}
}
