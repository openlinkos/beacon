package sshgate

import (
	"io"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/store"
)

// A recording without a terminal is replayed as a terminal shows it: a bare \n becomes \r\n in the
// output, also when its \r ended the event before; what is \r\n already, the header and the input
// stay as they are.
func TestReplayable(t *testing.T) {
	in := strings.Join([]string{
		`{"version":3,"term":{"cols":80,"rows":24},"command":"ls\n"}`,
		`[0.1,"o","a\nb\r\nc\r"]`,
		`[0.2,"i","typed\n"]`,
		`[0.000001,"o","\nd\n"]`,
		`[0.3,"x","0"]`,
	}, "\n") + "\n"
	b, err := io.ReadAll(replayable(io.NopCloser(strings.NewReader(in))))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`{"version":3,"term":{"cols":80,"rows":24},"command":"ls\n"}`,
		`[0.1,"o","a\r\nb\r\nc\r"]`,
		`[0.2,"i","typed\n"]`,
		`[0.000001,"o","\nd\r\n"]`,
		`[0.3,"x","0"]`,
	}, "\n") + "\n"
	if string(b) != want {
		t.Errorf("got\n%s\nwant\n%s", b, want)
	}
}

func TestTerminal(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		r    store.SessionRecord
		want bool
	}{
		{store.SessionRecord{Kind: "shell", PTY: &no}, false},
		{store.SessionRecord{Kind: "exec", PTY: &yes}, true},
		{store.SessionRecord{Kind: "shell"}, true}, // recorded before Beacon kept it
		{store.SessionRecord{Kind: "exec"}, false},
		{store.SessionRecord{Kind: "file"}, false},
	} {
		if got := terminal(c.r); got != c.want {
			t.Errorf("%+v: %v", c.r, got)
		}
	}
}
