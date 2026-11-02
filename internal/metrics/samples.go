package metrics

import (
	"strconv"
	"strings"
)

// Samples returns what the metric registered as name holds now, as a scrape would see it; nil for a
// name nobody registered. The console reads a few of them (when each probe location last wrote, say)
// rather than keeping a second record of the same thing.
func (m *Metrics) Samples(name string) []Sample {
	m.mu.Lock()
	var fn func() []Sample
	for _, g := range m.gauges {
		if g.name == name {
			fn = g.fn
		}
	}
	m.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// LabelValue returns the value of the label key in labels made with Labels, or "".
func LabelValue(labels, key string) string {
	s := strings.TrimSuffix(strings.TrimPrefix(labels, "{"), "}")
	for s != "" {
		name, rest, ok := strings.Cut(s, `="`)
		if !ok {
			return ""
		}
		// the value runs to the next quote that is not escaped
		end := -1
		for i := 0; i < len(rest); i++ {
			if rest[i] == '\\' {
				i++
				continue
			}
			if rest[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			return ""
		}
		if name == key {
			v, err := strconv.Unquote(`"` + rest[:end] + `"`)
			if err != nil {
				return ""
			}
			return v
		}
		s = strings.TrimPrefix(rest[end+1:], ",")
	}
	return ""
}
