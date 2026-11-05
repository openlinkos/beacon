package main

// The vault's hygiene wiring: the sweep the clock runs beside the grants' own — rotation notices
// for the items whose period ends, and the consequences for grants that sat unused. One file, so
// main.go keeps one line for each.

import (
	"log/slog"

	"github.com/openlinkos/beacon/internal/secrets"
)

// sweepHygiene runs the clock's hygiene work: the rotation notices and the idle grants'
// consequences (grant.on_unused).
func sweepHygiene(v *secrets.Service, log *slog.Logger) {
	if n := v.SweepHygiene(); n > 0 {
		log.Info("the secrets' hygiene sweep changed things", "changed", n)
	}
}
