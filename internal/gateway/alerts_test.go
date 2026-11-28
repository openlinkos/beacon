package gateway

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

const rulesToken = "rules-0123456789abcdef0123456789"

// TestAlertSendersAreNamed: the router learns who posted, the alerts:send token by its name and the
// rule engine as ""; the Watchdog and alerts of severity none from a token are dropped and counted.
func TestAlertSendersAreNamed(t *testing.T) {
	e := newEnv(t, true)
	body := `[{"labels":{"alertname":"Watchdog","severity":"none"}},
	          {"labels":{"alertname":"Quiet","service":"git","severity":"none"}},
	          {"labels":{"alertname":"ServiceDown","service":"git","severity":"critical"}}]`
	code(t, e.do("POST", "/api/v2/alerts", e.flux, body), http.StatusOK, "")
	if len(e.senders) != 1 || e.senders[0] != "flux" || len(e.posted) != 1 || e.posted[0].Labels["alertname"] != "ServiceDown" {
		t.Fatalf("from flux: senders %q, posted %+v", e.senders, e.posted)
	}
	if !strings.Contains(scrape(e), `beacon_gateway_alerts_dropped_total{token="flux"} 2`) {
		t.Errorf("dropped alerts not counted:\n%s", scrape(e))
	}
	code(t, e.do("POST", "/api/v2/alerts", rulesToken, body), http.StatusOK, "")
	if len(e.senders) != 2 || e.senders[1] != "" || len(e.posted) != 4 {
		t.Fatalf("from the rule engine: senders %q, posted %d", e.senders, len(e.posted))
	}
}

// TestAlertSendersAreLimited: an alerts:send token posts alertLimit times a minute, then gets 429
// alerts.too_many, counted; the rule engine is never held up, and nor is another token.
func TestAlertSendersAreLimited(t *testing.T) {
	e := newEnv(t, true)
	body := `[{"labels":{"alertname":"ServiceDown","service":"git"}}]`
	for i := range alertLimit {
		if rec := e.do("POST", "/api/v2/alerts", e.flux, body); rec.Code != http.StatusOK {
			t.Fatalf("post %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	code(t, e.do("POST", "/api/v2/alerts", e.flux, body), http.StatusTooManyRequests, "alerts.too_many")
	for range alertLimit + 5 {
		code(t, e.do("POST", "/api/v2/alerts", rulesToken, body), http.StatusOK, "")
	}
	if !strings.Contains(scrape(e), `beacon_gateway_alerts_limited_total{token="flux"} 1`) {
		t.Errorf("the refusal is not counted:\n%s", scrape(e))
	}
}

// TestAlertPostsAreCapped: one post from an alerts:send token passes at most alertsPerPost alerts; those
// beyond are dropped and counted. The rule engine's posts pass whole.
func TestAlertPostsAreCapped(t *testing.T) {
	e := newEnv(t, true)
	var alerts []string
	for i := range alertsPerPost + 50 {
		alerts = append(alerts, `{"labels":{"alertname":"ServiceDown","service":"svc-`+strconv.Itoa(i)+`"}}`)
	}
	body := "[" + strings.Join(alerts, ",") + "]"
	code(t, e.do("POST", "/api/v2/alerts", e.flux, body), http.StatusOK, "")
	if len(e.posted) != alertsPerPost || e.posted[alertsPerPost-1].Labels["service"] != "svc-"+strconv.Itoa(alertsPerPost-1) {
		t.Fatalf("passed %d alerts", len(e.posted))
	}
	if !strings.Contains(scrape(e), `beacon_gateway_alerts_capped_total{token="flux"} 50`) {
		t.Errorf("the dropped alerts are not counted:\n%s", scrape(e))
	}
	code(t, e.do("POST", "/api/v2/alerts", rulesToken, body), http.StatusOK, "")
	if len(e.posted) != 2*alertsPerPost+50 {
		t.Fatalf("the rule engine's post: %d alerts in all", len(e.posted))
	}
}
