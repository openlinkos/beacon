// Package exercise verifies observations of Beacon's deliberately failing drill endpoint.
// An observation is evidence that a location saw the deliberate response, independent of alert origin.
package exercise

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	Path               = "/drill/healthz"
	ChallengeHeader    = "X-Beacon-Exercise-Challenge"
	SampleHeader       = "X-Beacon-Exercise-Sample"
	ReceiptHeader      = "X-Beacon-Exercise-Receipt"
	ObservationPath    = "/api/v1/agent/exercise-observations"
	ActiveAtAnnotation = "beacon_rule_active_at"

	MaxToken = 2048 // upper bound for an exercise token in either direction
)

const challengeTTL = 90 * time.Second

// Run identifies an activation of the built-in drill endpoint.
type Run struct {
	ID    string    `json:"id,omitempty"`
	Armed time.Time `json:"armed_at,omitzero"`
	Until time.Time `json:"until,omitzero"`
	Ended time.Time `json:"ended_at,omitzero"`
}

func (r Run) Active(now time.Time) bool { return r.Ended.IsZero() && now.Before(r.Until) }

// Target identifies one exact configured check and its labels at assignment time.
type Target struct {
	URL        string `json:"target"`
	Module     string `json:"module"`
	Assignment string `json:"assignment"`
}

// Challenge is an optional addition to the enrolled probe's settings.
type Challenge struct {
	Target string `json:"target"`
	Module string `json:"module"`
	Token  string `json:"challenge"`
}

// Observation is a verified report through an enrolled location.
type Observation struct {
	Target
	Run      string    `json:"run"`
	Location string    `json:"location"`
	Sampled  time.Time `json:"sample_at"`
	Served   time.Time `json:"served_at"`
	Recorded time.Time `json:"recorded_at"`
}

type claims struct {
	Target
	Run     string `json:"run"`
	Place   string `json:"location"`
	Binding string `json:"binding"`
	Until   int64  `json:"until"`
	Sample  int64  `json:"sample,omitempty"`
	Served  int64  `json:"served,omitempty"`
}

// Authority signs narrowly purposed challenges and receipts with the centre's own key.
type Authority struct{ key []byte }

func New(key []byte) *Authority { return &Authority{key: key} }

// Assignment identifies the exact configured label set of an observation.
func Assignment(labels map[string]string) string {
	b, _ := json.Marshal(labels)
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

// Endpoint is the exact HTTPS URL at the configured public origin that may issue drill receipts.
func Endpoint(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return ""
	}
	u.Path = Path
	return u.String()
}

// Issue binds the gateway-authenticated location and credential to its assigned target.
func (a *Authority) Issue(run Run, target Target, location, credential string, now time.Time) *Challenge {
	if run.ID == "" || !run.Active(now) || target.URL == "" || target.Module == "" || location == "" || credential == "" {
		return nil
	}
	c := claims{Target: target, Run: run.ID, Place: location, Binding: a.binding(credential),
		Until: min(now.Add(challengeTTL).UnixMilli(), run.Until.UnixMilli())}
	return &Challenge{Target: target.URL, Module: target.Module, Token: a.sign("challenge", c)}
}

// Respond returns a receipt for a challenge only on the real endpoint during its current run.
func (a *Authority) Respond(run Run, token, host, sample string, now time.Time) string {
	var c claims
	if a == nil || !run.Active(now) || !a.verify("challenge", token, &c) || c.Run != run.ID ||
		now.UnixMilli() >= c.Until {
		return ""
	}
	target, err := url.Parse(c.URL)
	if err != nil || !strings.EqualFold(host, target.Host) {
		return ""
	}
	at, err := strconv.ParseInt(sample, 10, 64)
	if err != nil || at < run.Armed.UnixMilli() || at > now.Add(5*time.Second).UnixMilli() ||
		at < now.Add(-30*time.Second).UnixMilli() {
		return ""
	}
	c.Sample, c.Served = at, now.UnixMilli()
	return a.sign("receipt", c)
}

// Accept verifies a response under the same authenticated credential and current assignment.
// The store checks the run again while recording this proof.
func (a *Authority) Accept(run Run, token string, target Target, location, credential string, now time.Time) (Observation, bool) {
	var c claims
	if a == nil || !a.verify("receipt", token, &c) || c.Run != run.ID || c.Target != target || c.Place != location ||
		!hmac.Equal([]byte(c.Binding), []byte(a.binding(credential))) || now.UnixMilli() > c.Until ||
		c.Sample == 0 || c.Served == 0 || c.Served >= run.Until.UnixMilli() ||
		(!run.Ended.IsZero() && c.Served > run.Ended.UnixMilli()) {
		return Observation{}, false
	}
	return Observation{Target: c.Target, Run: c.Run, Location: c.Place,
		Sampled: time.UnixMilli(c.Sample).UTC(), Served: time.UnixMilli(c.Served).UTC(), Recorded: now.UTC()}, true
}

var wire = base64.RawURLEncoding

func (a *Authority) digest(kind, value string) []byte {
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write([]byte("beacon/exercise/v1/" + kind + "\x00" + value))
	return mac.Sum(nil)
}

func (a *Authority) binding(credential string) string {
	return wire.EncodeToString(a.digest("credential", credential))
}

func (a *Authority) sign(kind string, c claims) string {
	body, _ := json.Marshal(c)
	payload := wire.EncodeToString(body)
	return payload + "." + wire.EncodeToString(a.digest(kind, payload))
}

func (a *Authority) verify(kind, token string, out *claims) bool {
	if len(token) > MaxToken {
		return false
	}
	body, signature, ok := strings.Cut(token, ".")
	proof, err := wire.DecodeString(signature)
	if !ok || err != nil || !hmac.Equal(proof, a.digest(kind, body)) {
		return false
	}
	data, err := wire.DecodeString(body)
	return err == nil && json.Unmarshal(data, out) == nil
}
