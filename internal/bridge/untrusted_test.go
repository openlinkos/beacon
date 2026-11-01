package bridge

import (
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	for _, c := range []struct {
		in, want  string
		max       int
		multiline bool
	}{
		{"plain text", "plain text", 300, false},
		{"\x1b[31mred\x1b[0m and \x1b]8;;https://evil\x07link\x1b]8;;\x07", "red and link", 300, false},
		{"bidi \u202egnp.exe\u202c, zero\u200bwidth, \ufeffbom", "bidi gnp.exe, zerowidth, bom", 300, false},
		{"tags \U000E0049\U000E0047\U000E004E hidden", "tags hidden", 300, false},
		{"bell\x07 nul\x00 del\x7f c1\u0085", "bell nul del c1", 300, false},
		{"two\nlines\r\n\tand  spaces", "two lines and spaces", 300, false},
		{"two\nlines\r\n\tkept\u2028sep", "two\nlines\n\tkept\nsep", 300, true},
		{"bad \xff utf-8", "bad \ufffd utf-8", 300, false},
		{strings.Repeat("a", 10), "aaaa\u2026", 5, false},
		{"\u03b1\u03b2\u03b3\u03b4\u03b5\u03b6", "\u03b1\u03b2\u03b3\u2026", 4, false},
	} {
		if got := clean(c.in, c.max, c.multiline); got != c.want {
			t.Errorf("clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := cleanLabels(map[string]string{"a\x1b[1m": "v\u200b"}); got["a"] != "v" {
		t.Errorf("labels: %v", got)
	}
	for in, want := range map[string]string{
		"https://ci.example/run/1": "https://ci.example/run/1", "javascript:alert(1)": "", "ftp://x": "", "/relative": "",
		"https://x.example/a b": "", "https://x.example/\x1b[0m": "",
	} {
		if got := cleanURL(in); got != want {
			t.Errorf("cleanURL(%q) = %q", in, got)
		}
	}
	if got := mdEscape("<at id=all></at> *b* _i_ [l](u) `c` ~s~ a&b \\"); strings.ContainsAny(got, "<>*_[]`~\\") {
		t.Errorf("markdown escaped: %s", got)
	}
	if got := chatSafe(`<at user_id="all">x</at>`); strings.ContainsAny(got, "<>") {
		t.Errorf("chat-safe: %s", got)
	}
}
