package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetrics(t *testing.T) {
	m := New("test")
	m.Inc("beacon_notifications_total", "Notifications sent", Labels("channel", "feishu", "result", "ok"))
	m.Inc("beacon_notifications_total", "Notifications sent", Labels("channel", "feishu", "result", "ok"))
	m.Register("beacon_expected_location", "Expected probe locations", "gauge", func() []Sample {
		return []Sample{{Labels: Labels("location", `we"ird`), Value: 1}}
	})
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`beacon_notifications_total{channel="feishu",result="ok"} 2`,
		"# TYPE beacon_notifications_total counter",
		`beacon_expected_location{location="we\"ird"} 1`,
		`beacon_build_info{version="test",go="`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
