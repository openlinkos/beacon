package gateway

import (
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/router"
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

// capPost keeps the first alertsPerPost alerts of a post from the alerts:send token, counting and
// logging (once for each token) those beyond.
func (g *gateway) capPost(sender string, in []router.Postable) []router.Postable {
	if len(in) <= alertsPerPost {
		return in
	}
	if g.Metrics != nil {
		for range len(in) - alertsPerPost {
			g.Metrics.Inc("beacon_gateway_alerts_capped_total", "Alerts dropped from an alerts:send post beyond the first "+strconv.Itoa(alertsPerPost),
				metrics.Labels("token", sender))
		}
	}
	if _, seen := g.capLogged.LoadOrStore(sender, true); !seen && g.Log != nil {
		g.Log.Warn("alerts dropped: a post from an alerts:send token carries more alerts than one post may", "token", sender,
			"limit", alertsPerPost, "sent", len(in), "see", "beacon_gateway_alerts_capped_total")
	}
	return in[:alertsPerPost]
}
