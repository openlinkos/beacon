package bridge

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/vm"
)

// The heartbeats an alert is about. HeartbeatStale and HeartbeatMissing carry a heartbeat label; the
// catalogue says what the heartbeat is and, when it names one, the machine its job runs on. That machine
// counts among the alert's machines, so the evidence and the timeline list the sessions and changes on
// it, those just before the alert included; VictoriaMetrics says when the job last succeeded.

const maxHeartbeats = 5

// lastSuccessQuery is the last success of the heartbeats sel matches, within 31 days as the rules read it:
// a heartbeat may arrive as a steady series or as one pushed sample.
const lastSuccessQuery = "max by (heartbeat) (last_over_time(beacon_heartbeat_last_success_timestamp_seconds{%s}[31d]))"

// HeartbeatView is a heartbeat an alert is about: its entry in the catalogue, whether the SSH channel has
// the machine it names (Enrolled, when it names one), and its last success at the end of the window. Hint
// says how to reach the machine. Error says what could not be found out.
type HeartbeatView struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	MaxAge      string     `json:"max_age,omitempty"` // as the catalogue writes it: 26h
	Severity    string     `json:"severity,omitempty"`
	Machine     string     `json:"machine,omitempty"`
	Enrolled    *bool      `json:"enrolled,omitempty"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	Hint        string     `json:"hint,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// heartbeatNames are the heartbeats g's labels name, in the form of the catalogue's names, at most five.
func heartbeatNames(g *store.Group) []string {
	var out []string
	for _, v := range labelValues(g, "heartbeat") {
		if catalog.ValidName(v) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out[:min(len(out), maxHeartbeats)]
}

// heartbeatMachines are the machines the catalogue names for g's heartbeats.
func (b *Bridge) heartbeatMachines(g *store.Group) []string {
	var out []string
	cat := b.d.Catalog()
	for _, n := range heartbeatNames(g) {
		if h, ok := cat.Heartbeat(n); ok && h.Machine != "" && !slices.Contains(out, h.Machine) {
			out = append(out, h.Machine)
		}
	}
	return out
}

// heartbeatHint is what the hint of a firing call adds when the catalogue names the machine of the
// heartbeat the alert is about; "" otherwise.
func (b *Bridge) heartbeatHint(g *store.Group) string {
	ms := b.heartbeatMachines(g)
	if len(ms) == 0 {
		return ""
	}
	return " The catalogue says the heartbeat's job runs on machine " + strings.Join(ms, ", ") +
		": run_command and read_file reach it through Beacon, and list_machines gives its ssh_config for your own ssh."
}

// heartbeats describes g's heartbeats at the end of w; nil when its labels name none.
func (b *Bridge) heartbeats(ctx context.Context, g *store.Group, w Window) []HeartbeatView {
	names := heartbeatNames(g)
	if len(names) == 0 {
		return nil
	}
	last, qerr := b.lastSuccess(ctx, names, w.To)
	cat := b.d.Catalog()
	out := make([]HeartbeatView, 0, len(names))
	for _, n := range names {
		v := HeartbeatView{Name: n}
		var errs []string
		if t, ok := last[n]; ok {
			v.LastSuccess = &t
		}
		h, ok := cat.Heartbeat(n)
		switch {
		case !ok:
			errs = append(errs, "the catalogue has no heartbeat "+n)
		case h.Machine == "":
			v.Description, v.MaxAge, v.Severity = clean(h.Description, maxLong, true), h.MaxAge.String(), h.Severity
			v.Hint = "The catalogue names no machine for this heartbeat; its description may say where the job runs, and list_machines shows the machines you reach."
		default:
			v.Description, v.MaxAge, v.Severity, v.Machine = clean(h.Description, maxLong, true), h.MaxAge.String(), h.Severity, h.Machine
			_, enrolled := b.d.Store.Machine(h.Machine)
			v.Enrolled = &enrolled
			v.Hint = "The job runs on machine " + h.Machine + ": run_command and read_file reach it through Beacon, and list_machines gives " +
				"its ssh_config for your own ssh. The sessions and changes on it, those before the alert included, are under done.sessions and changes."
			if !enrolled {
				v.Hint = "The catalogue says the job runs on machine " + h.Machine + ", but the SSH channel has no such machine: Beacon cannot reach it."
			}
		}
		if qerr != "" {
			errs = append(errs, qerr)
		}
		v.Error = strings.Join(errs, "; ")
		out = append(out, v)
	}
	return out
}

// LastSuccess reads when each of the heartbeats called names last succeeded, as VictoriaMetrics knows
// it now (within 31 days); the error, if the query failed, is said in a line.
func (b *Bridge) LastSuccess(ctx context.Context, names []string) (map[string]time.Time, string) {
	if len(names) == 0 {
		return map[string]time.Time{}, ""
	}
	return b.lastSuccess(ctx, names, b.d.Now())
}

// lastSuccess reads when each of the heartbeats called names last succeeded, as of at; the error, if
// the query failed, is said in a line.
func (b *Bridge) lastSuccess(ctx context.Context, names []string, at time.Time) (map[string]time.Time, string) {
	if b.d.VM == nil {
		return nil, "VictoriaMetrics is not configured"
	}
	sel, ok := labelSelector("heartbeat", names)
	if !ok {
		return nil, "the heartbeat's name cannot be put into a query safely"
	}
	ctx, cancel := context.WithTimeout(ctx, evidenceTimeout)
	defer cancel()
	q := fmt.Sprintf(lastSuccessQuery, sel)
	var ss []vm.Sample
	var err error
	if rq, ok := b.d.VM.(RangeQuerier); ok {
		ss, err = rq.QueryAt(ctx, q, at)
	} else {
		ss, err = b.d.VM.Query(ctx, q)
	}
	if err != nil {
		return nil, "VictoriaMetrics: " + vmError(err)
	}
	out := map[string]time.Time{}
	for _, s := range ss {
		if s.Value > 0 && !math.IsNaN(s.Value) && !math.IsInf(s.Value, 0) {
			out[s.Labels["heartbeat"]] = time.Unix(int64(s.Value), 0).UTC()
		}
	}
	return out, ""
}
