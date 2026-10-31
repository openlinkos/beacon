package catalog

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/metrics"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndExport(t *testing.T) {
	d := t.TempDir()
	write(t, d, "locations.yaml", `
locations:
  - {id: us-los-angeles, name: United States - Los Angeles, region: overseas, network: CN2 GIA}
  - {id: cn-shanghai-bgp, name: Mainland China - Shanghai BGP, region: mainland, network: BGP}
`)
	write(t, d, "heartbeats.yaml", "heartbeats:\n  - {name: lo1-backup, max_age: 26h, severity: warning}\n")
	write(t, d, "resources.yaml", "resources:\n  - {name: cdn-pre-us3, kind: vps, expires: 2026-11-27}\n")
	write(t, d, "notes.txt", "ignored")
	c, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Locations) != 2 || len(c.Heartbeats) != 1 || len(c.Resources) != 1 {
		t.Fatalf("loaded %+v", c)
	}
	if l, ok := c.Location("cn-shanghai-bgp"); !ok || l.Region != "mainland" {
		t.Errorf("Location: %+v", l)
	}
	m := metrics.New("test")
	Register(m, func() *Catalog { return c })
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`linkos_expected_location{location="us-los-angeles",region="overseas",network="CN2 GIA"} 1`,
		`linkos_heartbeat_max_age_seconds{heartbeat="lo1-backup",severity="warning"} 93600`,
		`linkos_resource_expiry_timestamp_seconds{resource="cdn-pre-us3",kind="vps"} 1.7957376e+09`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
}

func TestLoadMissingDir(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(c.Locations) != 0 {
		t.Fatalf("a missing directory is an empty catalogue: %v %v", c, err)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]string{
		"bad id":         "locations: [{id: US_LA, name: x, region: overseas}]",
		"no name":        "locations: [{id: us-la, region: overseas}]",
		"bad region":     "locations: [{id: us-la, name: x, region: earth}]",
		"duplicate":      "locations: [{id: a, name: x, region: overseas}, {id: a, name: y, region: overseas}]",
		"short max age":  "heartbeats: [{name: a, max_age: 10s, severity: warning}]",
		"bad severity":   "heartbeats: [{name: a, max_age: 1h, severity: page}]",
		"bad date":       "resources: [{name: a, kind: vps, expires: 27/11/2026}]",
		"bad kind":       "resources: [{name: a, kind: car, expires: 2026-11-27}]",
		"unknown field":  "locations: [{id: a, name: x, region: overseas, vendor: acme}]",
		"duplicate hb":   "heartbeats: [{name: a, max_age: 1h, severity: warning}, {name: a, max_age: 2h, severity: warning}]",
		"unnamed res":    "resources: [{kind: vps, expires: 2026-11-27}]",
		"duplicate res":  "resources: [{name: a, kind: vps, expires: 2026-11-27}, {name: a, kind: vps, expires: 2026-11-28}]",
		"hb bad name":    "heartbeats: [{name: LO1, max_age: 1h, severity: warning}]",
		"region missing": "locations: [{id: a, name: x}]",
	}
	for name, body := range cases {
		d := t.TempDir()
		write(t, d, "c.yaml", body)
		if _, err := Load(d); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestExpectedFollowsTheMachine(t *testing.T) {
	d := t.TempDir()
	write(t, d, "c.yaml", `
locations:
  - {id: cdn-pre-us3, name: United States - Seattle 9929, region: overseas, network: "9929"}
  - {id: cdn-pro-us1, name: United States - Los Angeles Optimized 1, region: overseas, network: Optimized}
  - {id: monitor-us1, name: United States - Silicon Valley, region: overseas, network: BGP}
resources:
  - {name: cdn-pre-us3, kind: vps, expires: 2026-11-27}
  - {name: monitor-us1, kind: domain, expires: 2026-11-01}
`)
	c, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	scrape := func(at string) string {
		ts, err := time.Parse(time.RFC3339, at)
		if err != nil {
			t.Fatal(err)
		}
		now = func() time.Time { return ts }
		t.Cleanup(func() { now = time.Now })
		m := metrics.New("test")
		Register(m, func() *Catalog { return c })
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		return rec.Body.String()
	}
	seattle := `linkos_expected_location{location="cdn-pre-us3"`
	// no resource of that name: always expected; a resource of that name that is not a vps does not count
	others := []string{`linkos_expected_location{location="cdn-pro-us1"`, `linkos_expected_location{location="monitor-us1"`}
	// the whole expiry day still counts; the next day the probe is no longer expected
	b := scrape("2026-11-27T23:59:00Z")
	if !strings.Contains(b, seattle) {
		t.Errorf("on the expiry day the location is expected:\n%s", b)
	}
	b = scrape("2026-11-28T00:00:00Z")
	if strings.Contains(b, seattle) {
		t.Errorf("after its machine expires the location is no longer expected:\n%s", b)
	}
	for _, o := range others {
		if !strings.Contains(b, o) {
			t.Errorf("%s should still be expected:\n%s", o, b)
		}
	}
}
