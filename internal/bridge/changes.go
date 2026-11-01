package bridge

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// Change events are what changed around an alert: deployments, Flux reconciliations, configuration
// changes. Senders (CI, Flux, scripts) post them to the gateway with a changes:write token; the evidence
// and the timeline of an alert show those around it. They are data, like metrics: kept for
// changes.keep, not echoed to the chat; who posted each is kept with it.

var (
	sourceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`)
	kindRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)
)

const (
	defaultKeepChanges = 30 * 24 * time.Hour
	changeAhead        = time.Hour // a change dated further ahead comes from a wrong clock
)

func (b *Bridge) keepChanges() time.Duration {
	if k := b.d.Config.Changes.Keep.D(); k > 0 {
		return k
	}
	return defaultKeepChanges
}

// RecordChange stores a change event that the token called by posted. What its sender says is cleaned
// and cut, being untrusted; a change without a time happened now.
func (b *Bridge) RecordChange(_ context.Context, c store.Change, by string) (store.Change, error) {
	now := b.now()
	keep := b.keepChanges()
	c.Source, c.Kind, c.Machine, c.URL = strings.TrimSpace(c.Source), strings.TrimSpace(c.Kind), strings.TrimSpace(c.Machine), strings.TrimSpace(c.URL)
	if c.Kind == "" {
		c.Kind = "change"
	}
	if c.Time.IsZero() {
		c.Time = now
	}
	var why string
	switch {
	case !sourceRe.MatchString(c.Source):
		why = "source names the sender, such as flux or github-actions: letters, digits, dots, dashes, slashes and underscores, at most 64"
	case !kindRe.MatchString(c.Kind):
		why = "kind is a word such as deploy, config or rollback: letters, digits, dots, dashes, colons, slashes and underscores, at most 64"
	case strings.TrimSpace(c.Summary) == "":
		why = "summary is required: say what changed"
	case c.Machine != "" && !store.MachineName.MatchString(c.Machine):
		why = "machine is a machine's name: lower case letters, digits and dashes"
	case c.URL != "" && cleanURL(c.URL) == "":
		why = "url is an absolute http(s) URL"
	case c.Time.After(now.Add(changeAhead)):
		why = "time is more than an hour ahead of Beacon's clock"
	case c.Time.Before(now.Add(-keep)):
		why = "time is older than changes are kept (" + keep.String() + ")"
	}
	if why != "" {
		return store.Change{}, fmt.Errorf("%w: %s", ErrBadInput, why)
	}
	c.Time = c.Time.UTC()
	c.Summary = clean(c.Summary, maxLong, true)
	c.Service = clean(c.Service, maxShort, false)
	c.Revision = clean(c.Revision, maxShort, false)
	c.Object = cleanLine(c.Object)
	c.Severity = clean(c.Severity, 20, false)
	c.URL = cleanURL(c.URL)
	c.PostedBy, c.Received = by, now.UTC()
	return b.d.Store.PutChange(c, now.Add(-keep))
}

// ChangeQuery asks for change events. Since is how far back, as a duration (24h by default; 7d works
// too) or an RFC 3339 time.
type ChangeQuery struct {
	Service, Machine, Source, Since string
	Limit                           int
}

// Changes lists change events, the newest first.
func (b *Bridge) Changes(q ChangeQuery) ([]store.Change, error) {
	from, err := b.since(q.Since, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 1000 {
		return nil, fmt.Errorf("%w: limit is 1 to 1000", ErrBadInput)
	}
	if cut := b.now().Add(-b.keepChanges()); from.Before(cut) {
		from = cut // older ones are about to go
	}
	out := b.d.Store.Changes(store.ChangeFilter{From: from, Service: q.Service, Machine: q.Machine, Source: q.Source, Limit: q.Limit})
	if out == nil {
		out = []store.Change{}
	}
	return out, nil
}

// since reads how far back to look: a duration (with d for days) or an RFC 3339 time; empty is def.
func (b *Bridge) since(s string, def time.Duration) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return b.now().Add(-def), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if n, ok := strings.CutSuffix(s, "d"); ok {
		if days, err := strconv.Atoi(n); err == nil && days >= 0 && days <= 3660 {
			return b.now().Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return b.now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("%w: since is a duration such as 2h or 7d, or a time such as 2026-11-02T10:00:00Z", ErrBadInput)
}
