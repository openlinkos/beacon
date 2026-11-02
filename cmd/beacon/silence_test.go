package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/alertmanager"
)

func TestParseMatchers(t *testing.T) {
	m, err := parseMatchers(" alertname=ServiceDown, service=example.com ,note=a=b")
	if err != nil || len(m) != 3 || m["alertname"] != "ServiceDown" || m["service"] != "example.com" || m["note"] != "a=b" {
		t.Fatalf("parseMatchers: %v %v", m, err)
	}
	for _, bad := range []string{"", ",", "alertname", "=x", "bad-name=x", "service=", "a=1,a=2"} {
		if _, err := parseMatchers(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	ms := []alertmanager.Matcher{{Name: "alertname", Value: "ServiceDown", IsEqual: true}, {Name: "service", Value: "a|b", IsRegex: true, IsEqual: true},
		{Name: "severity", Value: "info", IsEqual: false}}
	if got := matchersText(ms); got != "alertname=ServiceDown,service=~a|b,severity!=info" {
		t.Errorf("matchersText: %q", got)
	}
}

func TestSilenceUsage(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{
		{"silence"},
		{"silence", "sideways"},
		{"silence", "create"},
		{"silence", "create", "-duration", "1h", "-comment", "x"},                                     // neither matchers nor alert
		{"silence", "create", "-matchers", "a=b", "-alert", "g1", "-duration", "1h", "-comment", "x"}, // both
		{"silence", "create", "-matchers", "a=b", "-duration", "1h"},                                  // no comment
		{"silence", "create", "-matchers", "a=b", "-comment", "x"},                                    // no duration
		{"silence", "create", "-matchers", "nonsense", "-duration", "1h", "-comment", "x"},            // bad matchers
		{"silence", "create", "-matchers", "a=b", "-duration", "1h", "-comment", "x", "extra"},        // a stray argument
		{"silence", "list", "extra"},
		{"silence", "expire"},
	} {
		errb.Reset()
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, errb.String())
		}
	}
	if !strings.Contains(errb.String(), "silence expire ID") {
		t.Errorf("usage: %s", errb.String())
	}
}
