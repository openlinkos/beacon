package metrics

import "testing"

func TestSamples(t *testing.T) {
	m := New("test")
	m.Register("x_last", "help", "gauge", func() []Sample {
		return []Sample{{Labels: Labels("subject", `a"b\c`, "region", "overseas"), Value: 7}}
	})
	got := m.Samples("x_last")
	if len(got) != 1 || got[0].Value != 7 {
		t.Fatalf("samples %v", got)
	}
	if v := LabelValue(got[0].Labels, "subject"); v != `a"b\c` {
		t.Errorf("subject %q", v)
	}
	if v := LabelValue(got[0].Labels, "region"); v != "overseas" {
		t.Errorf("region %q", v)
	}
	if v := LabelValue(got[0].Labels, "none"); v != "" {
		t.Errorf("a missing label is %q", v)
	}
	if m.Samples("unknown") != nil {
		t.Error("an unknown metric has samples")
	}
}
