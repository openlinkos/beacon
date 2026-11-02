package link

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newSigner(t *testing.T, now *time.Time) *Signer {
	t.Helper()
	s, err := NewSigner(bytes.Repeat([]byte{7}, KeySize), time.Hour, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSignAndVerify(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s := newSigner(t, &now)
	if _, err := NewSigner([]byte("short"), 0, nil); err == nil {
		t.Error("a short key is refused")
	}
	path := AlertPath("0cdfbada62dd")
	sig := s.Sign(http.MethodGet, path)
	until, ok := s.Verify(http.MethodGet, path, sig)
	if !ok || !until.Equal(now.Add(time.Hour)) {
		t.Fatalf("a fresh signature verifies: %v %v", ok, until)
	}
	exp, enc, _ := strings.Cut(sig, ".")
	bad := map[string]string{
		"wrong path":     sig,
		"wrong method":   sig,
		"other expiry":   "1900000000." + enc,
		"padded expiry":  "0" + exp + "." + enc,
		"signed expiry+": "+" + exp + "." + enc,
		"tampered":       exp + "." + enc[:len(enc)-2] + "AA",
		"no dot":         exp + enc,
		"not base64":     exp + ".%%%",
		"empty":          "",
	}
	for name, v := range bad {
		method, p := http.MethodGet, path
		switch name {
		case "wrong path":
			p = AlertPath("0cdfbada62de")
		case "wrong method":
			method = http.MethodPost
		}
		if _, ok := s.Verify(method, p, v); ok {
			t.Errorf("%s verifies", name)
		}
	}
	now = now.Add(time.Hour) // the very second it expires
	if _, ok := s.Verify(http.MethodGet, path, sig); ok {
		t.Error("an expired signature verifies")
	}
	now = now.Add(-2 * time.Hour)
	if _, ok := s.Verify(http.MethodGet, path, sig); !ok {
		t.Error("a signature made for later still verifies before its expiry")
	}
	if _, ok := s.Verify(http.MethodGet, path, s.SignUntil(http.MethodGet, path, now.Add(-time.Second))); ok {
		t.Error("SignUntil in the past verifies")
	}
	other, _ := NewSigner(bytes.Repeat([]byte{8}, KeySize), time.Hour, func() time.Time { return now })
	if _, ok := other.Verify(http.MethodGet, path, sig); ok {
		t.Error("another key verifies the signature")
	}
}

func TestPages(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s := newSigner(t, &now)
	p := &Pages{Signer: s, PublicURL: "https://beacon.example.com/"}
	u := p.Alert("0cdfbada62dd")
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host != "beacon.example.com" || parsed.Path != "/alerts/0cdfbada62dd" {
		t.Fatalf("alert link %q: %v", u, err)
	}
	if _, ok := s.Verify(http.MethodGet, parsed.Path, parsed.Query().Get(Param)); !ok {
		t.Errorf("the link's signature does not verify: %q", u)
	}
	if u := p.Session("20261102-100000-abcdef-1"); !strings.HasPrefix(u, "https://beacon.example.com/sessions/20261102-100000-abcdef-1?s=") {
		t.Errorf("session link %q", u)
	}
	for _, id := range []string{"", "../x", "a/b", "a?b", "a b", strings.Repeat("a", 129)} {
		if u := p.Alert(id); u != "" {
			t.Errorf("an id like %q gets a link: %q", id, u)
		}
	}
	var none *Pages
	if none.Alert("x") != "" || (&Pages{Signer: s}).Alert("x") != "" || (&Pages{PublicURL: "https://x"}).Alert("x") != "" {
		t.Error("without a signer or a public URL there are no links")
	}
	if p.URL("alerts/x") != "" {
		t.Error("a path without a leading slash gets a link")
	}
}
