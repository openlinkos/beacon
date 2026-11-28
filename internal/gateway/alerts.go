package gateway

import (
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/store"
)

// alertsLimited refuses the request, counting it, when the alerts:send token has posted more than
// alertLimit times this minute (limit.go).
func (g *gateway) alertsLimited(w http.ResponseWriter, t store.Token) bool {
	if g.alertRate.allow(t.Name, g.Now(), alertLimit, alertWindow) {
		return false
	}
	if g.Metrics != nil {
		g.Metrics.Inc("beacon_gateway_alerts_limited_total", "Alert posts refused because the token posted more than "+strconv.Itoa(alertLimit)+" in a minute",
			metrics.Labels("token", t.Name))
	}
	fail(w, http.StatusTooManyRequests, "alerts.too_many", "this token posted alerts more than "+strconv.Itoa(alertLimit)+
		" times in a minute; send them in fewer requests, or wait for the minute to pass")
	return true
}
