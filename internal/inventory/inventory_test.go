package inventory

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/status/internal/catalog"
	"github.com/openlinkos/status/internal/metrics"
)

func TestRotation(t *testing.T) {
	d := t.TempDir()
	// Addresses are literal IPs here, so no DNS is needed except for the rotations, which are names
	// that a fake resolver cannot answer: test the logic through a catalogue whose rotations are IPs too.
	body := `
locations:
  - {id: cdn-pre-us1, name: a, region: overseas, network: x, address: 192.0.2.1, rotation: 192.0.2.1}
  - {id: cdn-pre-us3, name: b, region: overseas, network: x, address: 192.0.2.3, rotation: 192.0.2.1}
  - {id: cdn-old, name: c, region: overseas, network: x, address: 192.0.2.9, rotation: 192.0.2.9}
  - {id: monitor-us1, name: d, region: overseas, network: x}
resources:
  - {name: cdn-old, kind: vps, expires: 2026-01-01}
`
	if err := os.WriteFile(filepath.Join(d, "c.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	c := &Checker{Catalog: func() *catalog.Catalog { return cat }, Now: func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }}
	c.Check(context.Background())
	m := metrics.New("test")
	c.Register(m)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		`linkos_rotation_resolve_success{rotation="192.0.2.1"} 1`,
		`linkos_rotation_members{rotation="192.0.2.1"} 1`,
		`linkos_location_in_rotation{location="cdn-pre-us1",rotation="192.0.2.1"} 1`,
		`linkos_location_in_rotation{location="cdn-pre-us3",rotation="192.0.2.1"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// the expired machine is not checked, and the location without a rotation neither
	for _, not := range []string{"cdn-old", "192.0.2.9", "monitor-us1", "linkos_rotation_unknown_address{"} {
		if strings.Contains(out, not) {
			t.Errorf("should not contain %s:\n%s", not, out)
		}
	}
}

func TestUnknownAddress(t *testing.T) {
	d := t.TempDir()
	// the rotation answers 192.0.2.5, which no location claims
	body := "locations:\n  - {id: n1, name: a, region: overseas, network: x, address: 192.0.2.1, rotation: 192.0.2.5}\n"
	if err := os.WriteFile(filepath.Join(d, "c.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(d)
	if err != nil {
		t.Fatal(err)
	}
	c := &Checker{Catalog: func() *catalog.Catalog { return cat }}
	c.Check(context.Background())
	m := metrics.New("test")
	c.Register(m)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `linkos_rotation_unknown_address{rotation="192.0.2.5",address="192.0.2.5"} 1`) ||
		!strings.Contains(rec.Body.String(), `linkos_location_in_rotation{location="n1",rotation="192.0.2.5"} 0`) {
		t.Fatalf("%s", rec.Body)
	}
}

func TestRotationNeedsAddress(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "c.yaml"), []byte("locations:\n  - {id: n1, name: a, region: overseas, rotation: cdn.example}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Load(d); err == nil {
		t.Fatal("a rotation without an address must be refused")
	}
}
