package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSettingsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{
		{"settings"},
		{"settings", "sideways"},
		{"settings", "set"},
		{"settings", "set", "one"},
		{"settings", "set", "request.max_uses", "not json"},
		{"settings", "reset"},
		{"settings", "history", "extra"},
		{"settings", "revert"},
		{"settings", "revert", "zero"},
	} {
		out.Reset()
		errb.Reset()
		if code := settingsCmd(args[1:], &out, &errb); code != 2 || errb.Len() == 0 {
			t.Errorf("%v: code %d, stderr %q", args, code, errb.String())
		}
	}
	// commands that need the service fail cleanly when it is not there to dial
	for _, args := range [][]string{
		{"settings", "get"},
		{"settings", "get", "request.max_uses"},
		{"settings", "set", "request.max_uses", "50"},
		{"settings", "reset", "request.max_uses"},
		{"settings", "history"},
		{"settings", "revert", "3"},
		{"settings", "rebuild-head"},
	} {
		out.Reset()
		errb.Reset()
		if code := settingsCmd(args[1:], &out, &errb); code != 1 {
			t.Errorf("%v: without a service to dial, code %d not 1 (%q)", args, code, errb.String())
		}
	}
	if s := settingsUsage; !strings.Contains(s, "settings set KEY VALUE") {
		t.Errorf("the usage says how: %s", s)
	}
}
