package scrub

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

const fixtureScrub = "fixture-scrub-plugh-xyzzy-9"

func TestScrubHidesEncodings(t *testing.T) {
	s := New(AllEncodings...)
	s.Add("token", fixtureScrub)
	plain := s.Scrub("plain " + fixtureScrub)
	if strings.Contains(plain, fixtureScrub) || !strings.Contains(plain, Placeholder("token")) {
		t.Fatalf("plain: %q", plain)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(fixtureScrub))
	got := s.Scrub("b64 " + b64)
	if strings.Contains(got, b64) || !strings.Contains(got, Placeholder("token")) {
		t.Fatalf("base64: %q", got)
	}
	h := hex.EncodeToString([]byte(fixtureScrub))
	got = s.Scrub("hex " + h)
	if strings.Contains(got, h) || !strings.Contains(got, Placeholder("token")) {
		t.Fatalf("hex: %q", got)
	}
}
