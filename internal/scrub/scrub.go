// Package scrub hides secret values from text: every form a value takes — itself, its base64,
// its URL encoding, its hexadecimal — is replaced by a mark that names the secret, so an output, a
// recording or a transcript can be shown without handing the value over.
//
// What this stops is the accidental leak: a command printing its environment, an API echoing the
// credential it was called with. It does not stop an agent that means to leak a value — the command
// is the agent's own, and it can always wrap the value in an encoding this scrubber was not given or
// send it out some other way. That is the design's own words, and the reason the use paths are bound
// to machines and hosts that already hold the value.
package scrub

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// The encodings a value is hidden under, as the scrub.encodings setting names them.
const (
	Plain  = "plain"
	Base64 = "base64"
	URL    = "url"
	Hex    = "hex"
)

// AllEncodings lists the encodings in the order the setting's default has them.
var AllEncodings = []string{Plain, Base64, URL, Hex}

// Placeholder wraps a label as the mark that replaces a value: [secret:label].
func Placeholder(label string) string { return "[secret:" + label + "]" }

// Scrubber replaces the forms of the values it is given with marks that name them. Add every value
// before scrubbing text; the same scrubber can then be reused for a whole answer or session.
type Scrubber struct {
	encodings []string
	forms     []form
}

type form struct {
	text string
	mark string
}

// New makes a scrubber for the encodings named (unknown names are ignored; plain is always on, for
// a scrubber that did not hide the value itself would hide nothing).
func New(encodings ...string) *Scrubber {
	on := map[string]bool{Plain: true}
	kept := []string{Plain}
	for _, e := range encodings {
		if !on[e] && contains(AllEncodings, e) {
			on[e] = true
			kept = append(kept, e)
		}
	}
	return &Scrubber{encodings: kept}
}

// Encodings lists what this scrubber hides, plain first.
func (s *Scrubber) Encodings() []string { return append([]string(nil), s.encodings...) }

// Add registers one value under a label. Empty values are skipped: there is nothing to hide, and
// replacing the empty string would eat everything.
func (s *Scrubber) Add(label, value string) {
	if s == nil || value == "" {
		return
	}
	on := map[string]bool{}
	for _, e := range s.encodings {
		on[e] = true
	}
	add := func(text string) {
		if text != "" {
			s.forms = append(s.forms, form{text: text, mark: Placeholder(label)})
		}
	}
	add(value)
	if on[Base64] {
		add(base64.StdEncoding.EncodeToString([]byte(value)))
		add(base64.RawStdEncoding.EncodeToString([]byte(value)))
	}
	if on[URL] {
		if u := url.QueryEscape(value); u != value {
			add(u)
		}
		if u := url.PathEscape(value); u != value {
			add(u)
		}
	}
	if on[Hex] {
		h := hex.EncodeToString([]byte(value))
		add(h)
		add(strings.ToUpper(h))
	}
	// longest first, so a longer form (a base64 of the value, say) is replaced before a shorter
	// one it contains
	sort.SliceStable(s.forms, func(i, j int) bool { return len(s.forms[i].text) > len(s.forms[j].text) })
}

// Empty reports whether anything was added.
func (s *Scrubber) Empty() bool { return s == nil || len(s.forms) == 0 }

// Scrub replaces every form in text with its mark.
func (s *Scrubber) Scrub(text string) string {
	if s == nil || s.Empty() || text == "" {
		return text
	}
	for _, f := range s.forms {
		if strings.Contains(text, f.text) {
			text = strings.ReplaceAll(text, f.text, f.mark)
		}
	}
	return text
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Stream scrubs text that arrives in pieces — a command's output as the machine sends it. A value
// may straddle two pieces, so the last bytes that could hold the start of a form are held back until
// the next piece proves them safe; Flush releases what is left when the text ends. The marks it
// writes are the whole difference it makes: the text is otherwise passed through untouched.
type Stream struct {
	mu    sync.Mutex
	s     *Scrubber
	carry []byte
}

// NewStream wraps a scrubber for piecewise text.
func NewStream(s *Scrubber) *Stream {
	if s == nil {
		return nil
	}
	return &Stream{s: s}
}

// longest is the longest form the scrubber hides: no more than this many bytes can start a form.
func (s *Stream) longest() int {
	n := 0
	for _, f := range s.s.forms {
		if len(f.text) > n {
			n = len(f.text)
		}
	}
	return n
}

// Write scrubs one piece and returns what may safely pass on. Bytes held back come out with the
// next piece, or with Flush.
func (t *Stream) Write(p []byte) []byte {
	if t == nil {
		return p
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.s.Empty() {
		return p
	}
	data := append(t.carry, p...)
	t.carry = nil
	out := t.s.Scrub(string(data))
	// the tail could be the start of a form that the next piece completes: hold it back
	hold := min(t.longest()-1, len(out))
	t.carry = append([]byte(nil), out[len(out)-hold:]...)
	return []byte(out[:len(out)-hold])
}

// Flush returns what was held back, scrubbed a last time; after it the stream is empty.
func (t *Stream) Flush() []byte {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.s.Scrub(string(t.carry))
	t.carry = nil
	return []byte(out)
}
