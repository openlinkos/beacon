package gateway

import (
	"slices"
	"strconv"
)

// A sample line of the Prometheus text format, as the gateway reads it before VictoriaMetrics does. Only
// the plain form passes: a metric name, then labels with plain names and quoted values, then the value
// and perhaps a timestamp. VictoriaMetrics also reads other spellings (a name quoted inside the braces,
// a __name__ label that replaces the name before them), which would let a line say one metric to the
// gateway and another to VictoriaMetrics; a line in any of them is not a sample here.

// textSample is what a sample line says: its metric, its labels (values unescaped) and where the sample
// ends (a comment after it is cut).
type textSample struct {
	name   string
	labels [][2]string
	end    int
}

// label is the value of the label called name, and whether the line has it.
func (s textSample) label(name string) (string, bool) {
	for _, l := range s.labels {
		if l[0] == name {
			return l[1], true
		}
	}
	return "", false
}

// duplicate reports whether some label is given twice (VictoriaMetrics keeps the last), other than one
// of forced: the gateway sets those itself on whatever passes, after the sample's own, so that whichever
// the sample gives is replaced. It is meant for at most maxLabels labels, but takes time in proportion to
// their number however many there are.
func (s textSample) duplicate(forced []string) bool {
	if len(s.labels) <= 16 { // a few: compared with each other, without allocating
		for i := range s.labels {
			for j := range i {
				if s.labels[i][0] == s.labels[j][0] && !slices.Contains(forced, s.labels[i][0]) {
					return true
				}
			}
		}
		return false
	}
	seen := make(map[string]bool, len(s.labels))
	for _, l := range s.labels {
		if seen[l[0]] && !slices.Contains(forced, l[0]) {
			return true
		}
		seen[l[0]] = true
	}
	return false
}

// tooManyLabels reports whether the sample has more than maxLabels labels besides its name (__name__,
// in remote write).
func (s textSample) tooManyLabels() bool {
	n := 0
	for _, l := range s.labels {
		if l[0] != "__name__" {
			n++
		}
	}
	return n > maxLabels
}

// parseSample reads one line (without its line break) in the plain form, or reports that it is not one.
func parseSample(b []byte) (textSample, bool) {
	var s textSample
	i := skipBlank(b, 0)
	start := i
	if i >= len(b) || !nameStart(b[i]) {
		return s, false
	}
	for i++; i < len(b) && nameChar(b[i]); i++ {
	}
	s.name = string(b[start:i])
	i = skipBlank(b, i)
	if i < len(b) && b[i] == '{' {
		var ok bool
		if s.labels, i, ok = parseLabels(b, i+1); !ok {
			return s, false
		}
		i = skipBlank(b, i)
	} else if i == len(b) || (b[i-1] != ' ' && b[i-1] != '\t') {
		return s, false // a name runs straight into something else
	}
	// the value, then perhaps a timestamp
	v, i := token(b, i)
	if _, err := strconv.ParseFloat(string(v), 64); err != nil {
		return s, false
	}
	s.end = i
	if i = skipBlank(b, i); i < len(b) && b[i] != '#' {
		ts, j := token(b, i)
		if !integer(ts) {
			return s, false
		}
		s.end = j
		i = skipBlank(b, j)
	}
	if i < len(b) && b[i] != '#' {
		return s, false
	}
	return s, true
}

// parseLabels reads label="value" pairs up to the closing brace, from just after the opening one.
func parseLabels(b []byte, i int) ([][2]string, int, bool) {
	var out [][2]string
	for {
		i = skipBlank(b, i)
		if i < len(b) && b[i] == '}' {
			return out, i + 1, true
		}
		start := i
		if i >= len(b) || !labelStart(b[i]) {
			return nil, i, false
		}
		for i++; i < len(b) && labelChar(b[i]); i++ {
		}
		name := string(b[start:i])
		if i = skipBlank(b, i); i >= len(b) || b[i] != '=' {
			return nil, i, false
		}
		if i = skipBlank(b, i+1); i >= len(b) || b[i] != '"' {
			return nil, i, false
		}
		value, j, ok := quoted(b, i+1)
		if !ok {
			return nil, i, false
		}
		out = append(out, [2]string{name, value})
		if i = skipBlank(b, j); i < len(b) && b[i] == ',' {
			i++
		} else if i >= len(b) || b[i] != '}' {
			return nil, i, false
		}
	}
}

// quoted reads a label value up to its closing quote, from just after the opening one: \\, \" and \n
// are escapes, and a backslash before anything else stays, as VictoriaMetrics reads it.
func quoted(b []byte, i int) (string, int, bool) {
	var v []byte
	for ; i < len(b); i++ {
		switch c := b[i]; c {
		case '"':
			return string(v), i + 1, true
		case '\\':
			if i+1 >= len(b) {
				return "", i, false
			}
			i++
			switch b[i] {
			case '\\', '"':
				v = append(v, b[i])
			case 'n':
				v = append(v, '\n')
			default:
				v = append(v, '\\', b[i])
			}
		default:
			v = append(v, c)
		}
	}
	return "", i, false
}

func token(b []byte, i int) ([]byte, int) {
	start := i
	for i < len(b) && b[i] != ' ' && b[i] != '\t' {
		i++
	}
	return b[start:i], i
}

func integer(b []byte) bool {
	if len(b) > 0 && b[0] == '-' {
		b = b[1:]
	}
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func skipBlank(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return i
}

func nameStart(c byte) bool {
	return c == '_' || c == ':' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}
func nameChar(c byte) bool   { return nameStart(c) || '0' <= c && c <= '9' }
func labelStart(c byte) bool { return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func labelChar(c byte) bool  { return labelStart(c) || '0' <= c && c <= '9' }
