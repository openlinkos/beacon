package store

import "time"

// Note is a note left on an alert group by a person or an agent: what was found, what was done.
type Note struct {
	By   string    `json:"by"`
	Via  string    `json:"via"` // api or mcp
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// AgentCall is one call of an agent hook for a group: to wake an agent when the group fired, or to tell
// it that the group resolved. Delivery names the call and stays the same across its attempts.
type AgentCall struct {
	Hook     string    `json:"hook"`
	Kind     string    `json:"kind"`  // webhook or github
	Event    string    `json:"event"` // firing or resolved
	Delivery string    `json:"delivery"`
	Status   string    `json:"status"` // pending, ok or failed
	Attempts int       `json:"attempts"`
	Code     int       `json:"code,omitempty"`  // the HTTP status of the last attempt
	Error    string    `json:"error,omitempty"` // why the last attempt failed
	At       time.Time `json:"at"`              // when the call was made
	Done     time.Time `json:"done,omitzero"`
}
