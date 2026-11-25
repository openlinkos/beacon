package bridge

import (
	"errors"

	"github.com/openlinkos/beacon/internal/store"
)

// ErrDrillHookNotFound applies both to missing hooks and hooks assigned to a different reporter.
var ErrDrillHookNotFound = errors.New("no active drill hook assigned to this reporter")

// DrillHookCheck describes the loaded firing configuration. It makes no call to either receiver.
type DrillHookCheck struct {
	SignedWebhook bool   `json:"signed_webhook"`
	MatchesDrill  bool   `json:"matches_drill"`
	DrillOnly     bool   `json:"drill_only"`
	CriticalChat  string `json:"critical_chat"`
}

// CheckDrillHook checks only the named hook and only for its configured reporter.
func (b *Bridge) CheckDrillHook(name string, who Actor) (DrillHookCheck, error) {
	hook, ok := b.hook(name)
	if !ok || !b.agentReporter(name, hook.Reporter, who) {
		return DrillHookCheck{}, ErrDrillHookNotFound
	}
	group := store.Group{Severity: "critical", Labels: map[string]string{"service": "drill"}}
	return DrillHookCheck{
		SignedWebhook: hook.Kind == "webhook" && hook.Secret != "",
		MatchesDrill:  b.takes(hook, &group, "firing"),
		DrillOnly:     hook.Service == "drill" || hook.Service == "^drill$",
		CriticalChat:  b.Delivery().CriticalChat,
	}, nil
}
