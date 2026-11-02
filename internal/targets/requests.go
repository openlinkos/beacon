package targets

// Probing on demand, an addition to the probe protocol: an agent at the centre asks for a target to be
// probed again at once, and every location that has the target probes it and posts what it found,
// besides pushing the samples as its cycle does. A probe long-polls GET /api/v1/agent/requests?wait=25s,
// which answers at once with the requests pending for its location, or with an empty list when the wait
// passes, and posts each result to POST /api/v1/agent/requests/{id}. A probe that does not know the
// routes never polls; a gateway that lacks them answers 404, and the probe asks again in an hour.

// Request is one request as the gateway hands it to a probe: the targets to probe now, in the groups
// the probe already understands (the labels carry the module), only those this location has assigned.
type Request struct {
	ID     string  `json:"id"`
	Groups []Group `json:"groups"`
}

// Answer is what a probe posts back for a request: one result per target.
type Answer struct {
	Results []TargetResult `json:"results"`
}

// TargetResult is what one probe of one target found, the fields the evidence shows. CertExpiry is
// unix seconds, 0 when no certificate was seen; Attempts counts the tries, with the location's retries.
type TargetResult struct {
	Target          string  `json:"target"`
	Module          string  `json:"module"`
	Success         bool    `json:"success"`
	Reason          string  `json:"reason,omitempty"`
	DurationSeconds float64 `json:"duration_seconds"`
	HTTPStatus      int     `json:"http_status,omitempty"`
	CertExpiry      int64   `json:"cert_expiry,omitempty"`
	Attempts        int     `json:"attempts,omitempty"`
}
