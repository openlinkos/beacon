package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

// Machine access: which machines a credential reaches on the SSH channel, and whether it may change
// machines. It is always explicit. AccessAll is every machine that is not critical, machines added later
// included; AccessNamed is only the machines named. Anything else, a missing value included, reaches no
// machine, and so does an empty list of names: nothing ever falls back to all.
//
// A critical machine (Machine.Critical) is reached only by a credential that names it, whatever its
// access: all covers it only when it is also named. A named machine can carry an end (MachineUntil): from
// then on it is not reached, and TidyMachineAccess forgets it. A person reaches every machine that is not
// critical; an owner reaches every machine, critical ones included (AccessEvery), and anyone else only
// the critical machines named on their account. An agent never reaches more than its owner, and an
// OAuth grant never more than its agent.

// Machine access values.
const (
	// AccessAll is every machine that is not critical, including machines added later.
	AccessAll = "all"
	// AccessNamed is only the machines named.
	AccessNamed = "named"
	// AccessEvery is every machine, critical ones included: an owner's own access. No credential holds it.
	AccessEvery = "every"
)

// CriticalGrantTTL is how long an agent's credential reaches a critical machine it is newly given, unless
// whoever names the machine says otherwise. A person's own named machines do not end unless they say so.
const CriticalGrantTTL = 30 * 24 * time.Hour

// MachineAccess is a credential's machine access: Access, Machines named (critical ones, with AccessAll)
// and when the access to a named machine ends (none: it does not).
type MachineAccess struct {
	Access       string               `json:"machine_access,omitempty"`
	Machines     []string             `json:"machines,omitempty"`
	MachineUntil map[string]time.Time `json:"machine_until,omitempty"`
}

// AllAccess is AccessAll, naming the critical machines names.
func AllAccess(names ...string) MachineAccess {
	return MachineAccess{Access: AccessAll, Machines: names}.normal()
}

// NamedAccess is AccessNamed with names: none of them, no machine.
func NamedAccess(names ...string) MachineAccess {
	return MachineAccess{Access: AccessNamed, Machines: names}.normal()
}

// IsAll reports whether the access covers every machine that is not critical.
func (a MachineAccess) IsAll() bool { return a.Access == AccessAll || a.Access == AccessEvery }

// IsEvery reports whether the access covers every machine, critical ones included.
func (a MachineAccess) IsEvery() bool { return a.Access == AccessEvery }

// Names reports whether machine is named (whether or not its access has ended: see Live).
func (a MachineAccess) Names(machine string) bool { return slices.Contains(a.Machines, machine) }

// Until is when the access to the named machine ends; zero when it does not.
func (a MachineAccess) Until(machine string) time.Time { return a.MachineUntil[machine] }

// Covers reports whether the access covers machine, which is critical or not.
func (a MachineAccess) Covers(machine string, critical bool) bool {
	return a.IsEvery() || a.Names(machine) || a.IsAll() && !critical
}

// None reports whether the access reaches no machine at all.
func (a MachineAccess) None() bool { return !a.IsAll() && len(a.Machines) == 0 }

// Live is the access without the named machines whose access ended at now or before.
func (a MachineAccess) Live(now time.Time) MachineAccess {
	if len(a.MachineUntil) == 0 {
		return a.clone()
	}
	out := a.clone()
	for name, until := range a.MachineUntil {
		if !now.Before(until) {
			out.Machines = slices.DeleteFunc(out.Machines, func(m string) bool { return m == name })
			delete(out.MachineUntil, name)
		}
	}
	return out.normal()
}

// expired lists the named machines whose access ended at now or before.
func (a MachineAccess) expired(now time.Time) []string {
	var out []string
	for name, until := range a.MachineUntil {
		if a.Names(name) && !now.Before(until) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func (a MachineAccess) clone() MachineAccess {
	return MachineAccess{Access: a.Access, Machines: slices.Clone(a.Machines), MachineUntil: maps.Clone(a.MachineUntil)}
}

// normal sorts and compacts the names and keeps ends only for named machines. A missing access becomes
// AccessNamed: what is written is always explicit, and a write without names reaches nothing.
func (a MachineAccess) normal() MachineAccess {
	out := a.clone()
	if out.Access == "" {
		out.Access = AccessNamed
	}
	out.Machines = compactNames(out.Machines)
	for name, until := range out.MachineUntil {
		if !out.Names(name) || until.IsZero() {
			delete(out.MachineUntil, name)
		}
	}
	if len(out.MachineUntil) == 0 {
		out.MachineUntil = nil
	} else {
		for name, until := range out.MachineUntil {
			out.MachineUntil[name] = until.UTC()
		}
	}
	return out
}

// check refuses an access a credential may not hold: AccessEvery, an unknown value, a bad machine name.
func (a MachineAccess) check() error {
	switch a.Access {
	case AccessAll, AccessNamed:
	default:
		return fmt.Errorf("machine access %q: it is %s or %s", a.Access, AccessAll, AccessNamed)
	}
	for _, m := range a.Machines {
		if !MachineName.MatchString(m) {
			return fmt.Errorf("machine %q: names are lower case letters, digits and dashes", m)
		}
	}
	return nil
}

// Within reports whether a gives nothing beyond b: every machine a covers, b covers as long or longer.
// critical says which machines are critical now.
func (a MachineAccess) Within(b MachineAccess, critical func(string) bool) bool {
	switch {
	case b.IsEvery():
		return true
	case a.IsEvery():
		return false
	case a.IsAll() && !b.IsAll():
		return false
	}
	for _, m := range a.Machines {
		if b.IsAll() && !critical(m) {
			continue
		}
		if !b.Names(m) {
			return false
		}
		if ub := b.Until(m); !ub.IsZero() {
			if ua := a.Until(m); ua.IsZero() || ua.After(ub) {
				return false
			}
		}
	}
	return true
}

// IntersectAccess is what every layer covers: an agent's access within its owner's, a grant's within
// its agent's. Named machines carry the earliest end any layer gives them. critical says which machines
// are critical now.
func IntersectAccess(critical func(string) bool, layers ...MachineAccess) MachineAccess {
	if len(layers) == 0 {
		return NamedAccess()
	}
	out := MachineAccess{Access: AccessEvery}
	for _, l := range layers {
		switch {
		case l.IsEvery():
		case l.IsAll() && out.Access != AccessNamed:
			out.Access = AccessAll
		default:
			out.Access = AccessNamed
		}
	}
	var names []string
	for _, l := range layers {
		names = append(names, l.Machines...)
	}
	for _, m := range compactNames(names) {
		var until time.Time
		covered := true
		for _, l := range layers {
			if !l.Covers(m, critical(m)) {
				covered = false
				break
			}
			if u := l.Until(m); l.Names(m) && !u.IsZero() && (until.IsZero() || u.Before(until)) {
				until = u
			}
		}
		if !covered {
			continue
		}
		out.Machines = append(out.Machines, m)
		if !until.IsZero() {
			if out.MachineUntil == nil {
				out.MachineUntil = map[string]time.Time{}
			}
			out.MachineUntil[m] = until
		}
	}
	if out.IsEvery() {
		out.Machines, out.MachineUntil = nil, nil
	}
	return out
}

// String is the access for the audit log and the command line: all, all + db (until …), named box, db,
// none.
func (a MachineAccess) String() string {
	var names []string
	for _, m := range a.Machines {
		if u := a.Until(m); !u.IsZero() {
			m += " (until " + u.UTC().Format("2006-01-02 15:04Z") + ")"
		}
		names = append(names, m)
	}
	switch {
	case a.IsEvery():
		return "every machine"
	case a.IsAll() && len(names) == 0:
		return "all"
	case a.IsAll():
		return "all + " + strings.Join(names, ", ")
	case len(names) == 0:
		return "none"
	}
	return "named " + strings.Join(names, ", ")
}

// ---- the critical machines

func criticalIn(tx kv.Tx) map[string]bool {
	out := map[string]bool{}
	tx.Range(bucketMachines, kv.Range{}, func(_, v []byte) bool {
		var m Machine
		if json.Unmarshal(v, &m) == nil && m.Critical {
			out[m.Name] = true
		}
		return true
	})
	return out
}

// CriticalMachines are the names of the machines marked critical now.
func (s *Store) CriticalMachines() map[string]bool {
	var out map[string]bool
	_ = s.db.View(func(tx kv.Tx) error {
		out = criticalIn(tx)
		return nil
	})
	return out
}

// ---- the machines a list names, as the command line writes them

// ParseMachineList reads a machine access as the command line writes it: all, none, or names, separated
// by commas, each optionally with @END, when its access ends: a duration (72h, 30d) from now, a date
// (2026-12-31) or a time (RFC 3339), or never. all can be followed by named machines too (all,db@72h):
// critical machines all does not cover. Until holds the ends given; Forever the names given @never, so
// that a default end is not applied to them.
func ParseMachineList(text string, now time.Time) (access MachineAccess, forever []string, err error) {
	access = MachineAccess{Access: AccessNamed}
	for _, item := range strings.Split(text, ",") {
		item = strings.TrimSpace(item)
		name, end, hasEnd := strings.Cut(item, "@")
		switch {
		case item == "":
			continue
		case item == "all":
			access.Access = AccessAll
			continue
		case item == "none":
			continue
		case !MachineName.MatchString(name):
			return MachineAccess{}, nil, fmt.Errorf("machine %q: names are lower case letters, digits and dashes", name)
		}
		access.Machines = append(access.Machines, name)
		if !hasEnd {
			continue
		}
		if end == "never" {
			forever = append(forever, name)
			continue
		}
		until, err := parseEnd(end, now)
		if err != nil {
			return MachineAccess{}, nil, fmt.Errorf("machine %s: %w", name, err)
		}
		if access.MachineUntil == nil {
			access.MachineUntil = map[string]time.Time{}
		}
		access.MachineUntil[name] = until
	}
	return access.normal(), compactNames(forever), nil
}

// parseEnd reads when a named machine's access ends: a duration from now, a date or a time.
func parseEnd(text string, now time.Time) (time.Time, error) {
	if d, ok := strings.CutSuffix(text, "d"); ok {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			return now.Add(time.Duration(n) * 24 * time.Hour).UTC(), nil
		}
	}
	if d, err := time.ParseDuration(text); err == nil {
		if d < time.Hour {
			return time.Time{}, errors.New("the access ends at least an hour from now")
		}
		return now.Add(d).UTC(), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z07:00", time.DateOnly} {
		if t, err := time.Parse(layout, text); err == nil {
			if !t.After(now) {
				return time.Time{}, errors.New("the access must end in the future")
			}
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("@%s: write the end as a duration (72h, 30d), a date (2026-12-31), a time (RFC 3339) or never", text)
}

// WithCriticalEnds gives every critical machine newly named in a (not named in before) the end
// now + CriticalGrantTTL, unless it has one or is in forever. A machine named in before keeps its end
// unless a gives it another. It is for an agent's credentials; a person's own named machines do not end
// unless they say so.
func WithCriticalEnds(a, before MachineAccess, critical func(string) bool, forever []string, now time.Time) MachineAccess {
	out := a.clone()
	for _, m := range out.Machines {
		if _, given := out.MachineUntil[m]; given || slices.Contains(forever, m) {
			continue
		}
		var until time.Time
		switch {
		case before.Names(m):
			until = before.Until(m)
		case critical(m):
			until = now.Add(CriticalGrantTTL)
		}
		if !until.IsZero() {
			if out.MachineUntil == nil {
				out.MachineUntil = map[string]time.Time{}
			}
			out.MachineUntil[m] = until
		}
	}
	return out.normal()
}

// KeepEnds gives every machine named in a and in before the end it had in before, unless a gives it
// another: a change that keeps a machine keeps its end.
func KeepEnds(a, before MachineAccess) MachineAccess {
	out := a.clone()
	for _, m := range out.Machines {
		if _, given := out.MachineUntil[m]; given || !before.Names(m) || before.Until(m).IsZero() {
			continue
		}
		if out.MachineUntil == nil {
			out.MachineUntil = map[string]time.Time{}
		}
		out.MachineUntil[m] = before.Until(m)
	}
	return out.normal()
}

// ---- the move to explicit machine access, and named machines whose access ended

// metaMachineAccess marks the one-time move to explicit machine access as done.
var metaMachineAccess = []byte("machine_access_explicit")

// MachineAccessChange is one credential, or one person, whose machine access Beacon changed by itself:
// Kind is token, agent, grant or account, Name the token's, agent's or person's name or the grant's ID,
// and Access what it is now (for an account, the critical machines named on it).
type MachineAccessChange struct {
	Kind   string
	Name   string
	Access MachineAccess
	Ended  []string // TidyMachineAccess: the named machines whose access ended
}

func migratedAccess(old MachineAccess, critical []string) MachineAccess {
	if len(old.Machines) == 0 { // an empty list used to reach every machine
		return AllAccess(critical...)
	}
	return NamedAccess(old.Machines...)
}

// MigrateMachineAccess makes the machine access of credentials from before it was explicit explicit,
// once, so that what each reaches stays as it was: a token of its own with people's scopes, an agent or an
// OAuth grant that named no machines (which reached every machine) gets access to all machines and names
// every machine critical now, without an end; one that named machines keeps them. A person who is not an
// owner gets the critical machines of now named on their account. Tokens of machines and services reach
// no machine, as before. Each change is in the audit log, in the same transaction; a database moved
// already is left alone.
func (s *Store) MigrateMachineAccess() ([]MachineAccessChange, error) {
	var out []MachineAccessChange
	now := s.now().UTC()
	s.tokenMu.Lock()
	err := s.db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		out = nil
		if tx.Get(bucketMeta, metaMachineAccess) != nil {
			return nil
		}
		var critical []string
		for m := range criticalIn(tx) {
			critical = append(critical, m)
		}
		critical = compactNames(critical)
		record := func(c MachineAccessChange) error {
			out = append(out, c)
			detail := "machine access made explicit: " + c.Access.String()
			if c.Kind == "account" {
				detail = "critical machines named: " + strings.Join(c.Access.Machines, ", ")
			}
			return s.audit.AppendTx(raw, &events.Event{Time: now, Actor: "beacon", Via: "migration", Action: "machine_access_migrate",
				Object: c.Kind + ":" + c.Name, Detail: detail})
		}
		type put struct {
			bucket, key []byte
			v           any
		}
		var puts []put
		var fail error
		tx.Range(bucketTokens, kv.Range{}, func(k, v []byte) bool {
			var t Token
			if fail = json.Unmarshal(v, &t); fail != nil {
				return false
			}
			if t.Agent != "" || t.Access != "" {
				return true
			}
			people := slices.ContainsFunc(t.Scopes, func(sc string) bool { return slices.Contains(PeopleScopes, sc) })
			if !people { // a machine's or a service's token: it never reached a machine
				t.MachineAccess = NamedAccess()
			} else {
				t.MachineAccess = migratedAccess(t.MachineAccess, critical)
				if fail = record(MachineAccessChange{Kind: "token", Name: t.Name, Access: t.MachineAccess}); fail != nil {
					return false
				}
			}
			puts = append(puts, put{bucketTokens, bytesClone(k), t})
			return true
		})
		if fail != nil {
			return fail
		}
		tx.Range(bucketAgents, kv.Range{}, func(k, v []byte) bool {
			var a Agent
			if fail = json.Unmarshal(v, &a); fail != nil {
				return false
			}
			if a.Access != "" {
				return true
			}
			a.MachineAccess = migratedAccess(a.MachineAccess, critical)
			puts = append(puts, put{bucketAgents, bytesClone(k), a})
			fail = record(MachineAccessChange{Kind: "agent", Name: a.Name, Access: a.MachineAccess})
			return fail == nil
		})
		if fail != nil {
			return fail
		}
		tx.Range(bucketGrants, kv.Range{}, func(k, v []byte) bool {
			var g Grant
			if fail = json.Unmarshal(v, &g); fail != nil {
				return false
			}
			if g.Access != "" {
				return true
			}
			g.MachineAccess = migratedAccess(g.MachineAccess, critical)
			puts = append(puts, put{bucketGrants, bytesClone(k), g})
			fail = record(MachineAccessChange{Kind: "grant", Name: g.ID, Access: g.MachineAccess})
			return fail == nil
		})
		if fail != nil {
			return fail
		}
		tx.Range(bucketAccounts, kv.Range{}, func(k, v []byte) bool {
			var a Account
			if fail = json.Unmarshal(v, &a); fail != nil {
				return false
			}
			if a.Role == "owner" || len(critical) == 0 || len(a.CriticalMachines) > 0 {
				return true
			}
			a.CriticalMachines = slices.Clone(critical)
			puts = append(puts, put{bucketAccounts, bytesClone(k), a})
			fail = record(MachineAccessChange{Kind: "account", Name: a.Username, Access: a.CriticalAccess()})
			return fail == nil
		})
		if fail != nil {
			return fail
		}
		for _, p := range puts {
			b, err := json.Marshal(p.v)
			if err != nil {
				return err
			}
			if err := tx.Put(p.bucket, p.key, b); err != nil {
				return err
			}
		}
		return tx.Put(bucketMeta, metaMachineAccess, []byte(now.Format(time.RFC3339Nano)))
	})
	s.tokenMu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(out) > 0 {
		if err := s.RefreshTokens(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// TidyMachineAccess forgets the named machines whose access ended at now or before, on every token,
// agent, OAuth grant and account, and records each change in the audit log in the same transaction.
// Their access ended when the time came, whether or not this has run; this keeps what is shown and
// stored in step.
func (s *Store) TidyMachineAccess(now time.Time) ([]MachineAccessChange, error) {
	var out []MachineAccessChange
	s.tokenMu.Lock()
	err := s.db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		out = nil
		var fail error
		type put struct {
			bucket, key []byte
			v           any
		}
		var puts []put
		tidy := func(bucket, key []byte, kind, name string, a *MachineAccess, v any) bool {
			ended := a.expired(now)
			if len(ended) == 0 {
				return true
			}
			*a = a.Live(now)
			puts = append(puts, put{bucket, bytesClone(key), v})
			c := MachineAccessChange{Kind: kind, Name: name, Access: *a, Ended: ended}
			out = append(out, c)
			fail = s.audit.AppendTx(raw, &events.Event{Time: now.UTC(), Actor: "beacon", Via: "ssh", Action: "machine_access_end",
				Object: kind + ":" + name, Detail: "access ended to " + strings.Join(ended, ", ") + "; now " + a.String()})
			return fail == nil
		}
		tx.Range(bucketTokens, kv.Range{}, func(k, v []byte) bool {
			t := new(Token)
			if json.Unmarshal(v, t) != nil || t.Agent != "" {
				return true
			}
			return tidy(bucketTokens, k, "token", t.Name, &t.MachineAccess, t)
		})
		tx.Range(bucketAgents, kv.Range{}, func(k, v []byte) bool {
			a := new(Agent)
			if json.Unmarshal(v, a) != nil {
				return true
			}
			return tidy(bucketAgents, k, "agent", a.Name, &a.MachineAccess, a)
		})
		tx.Range(bucketGrants, kv.Range{}, func(k, v []byte) bool {
			g := new(Grant)
			if json.Unmarshal(v, g) != nil {
				return true
			}
			return tidy(bucketGrants, k, "grant", g.ID, &g.MachineAccess, g)
		})
		tx.Range(bucketAccounts, kv.Range{}, func(k, v []byte) bool {
			a := new(Account)
			if json.Unmarshal(v, a) != nil {
				return true
			}
			named := a.CriticalAccess()
			ok := tidy(bucketAccounts, k, "account", a.Username, &named, a)
			a.CriticalMachines, a.CriticalUntil = named.Machines, named.MachineUntil
			return ok
		})
		if fail != nil {
			return fail
		}
		for _, p := range puts {
			b, err := json.Marshal(p.v)
			if err != nil {
				return err
			}
			if err := tx.Put(p.bucket, p.key, b); err != nil {
				return err
			}
		}
		return nil
	})
	s.tokenMu.Unlock()
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(out, func(c MachineAccessChange) bool { return c.Kind == "token" }) {
		if err := s.RefreshTokens(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Summary says in a sentence which machines the access covers, for the API's readers.
func (a MachineAccess) Summary() string {
	var names []string
	for _, m := range a.Machines {
		if u := a.Until(m); !u.IsZero() {
			m += " (until " + u.UTC().Format("2006-01-02 15:04 UTC") + ")"
		}
		names = append(names, m)
	}
	switch {
	case a.IsEvery():
		return "every machine, critical ones included"
	case a.IsAll() && len(names) == 0:
		return "all machines that are not critical, including machines added later"
	case a.IsAll():
		return "all machines that are not critical, including machines added later; critical machines named: " + strings.Join(names, ", ")
	case len(names) == 0:
		return "no machines"
	}
	return "only the machines named: " + strings.Join(names, ", ")
}
