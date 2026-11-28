package store

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

// A machine marked critical is reached only by credentials that name it, and an agent's credential names
// a critical machine for CriticalGrantTTL unless an owner says otherwise. Names given while the machine
// was not critical, by whoever could give them then, would otherwise become critical names with no end:
// when a machine becomes critical, every token, agent and OAuth grant that names it keeps it until
// now + CriticalGrantTTL at the latest, each change in the audit log in the same transaction. A shorter
// end stays. People's own accounts are not changed: an owner names critical machines on them.

// keyedPut is a record to write after a walk: a PostgreSQL transaction runs no other statement while a
// Range has its rows open.
type keyedPut struct {
	bucket, key []byte
	v           any
}

// criticalEnds works out, in tx, the ends that machine's becoming critical at now gives the credentials
// that name it: the records to write, their audit entries, and whether a token is among them.
func criticalEnds(tx kv.Tx, machine string, now time.Time, actor, via string) (puts []keyedPut, entries []*events.Event, tokens bool) {
	until := now.Add(CriticalGrantTTL).UTC()
	end := func(bucket, key []byte, kind, name string, a *MachineAccess, v any) {
		if !a.Names(machine) {
			return
		}
		if u := a.Until(machine); !u.IsZero() && !u.After(until) {
			return
		}
		if a.MachineUntil == nil {
			a.MachineUntil = map[string]time.Time{}
		}
		a.MachineUntil[machine] = until
		puts = append(puts, keyedPut{bucket, bytesClone(key), v})
		entries = append(entries, &events.Event{Time: now.UTC(), Actor: actor, Via: via, Action: "machine_access_critical", Object: kind + ":" + name,
			Detail: "machine " + machine + " marked critical: access to it ends " + until.Format(time.RFC3339) + "; now " + a.String()})
		tokens = tokens || kind == "token"
	}
	tx.Range(bucketTokens, kv.Range{}, func(k, v []byte) bool {
		t := new(Token)
		if json.Unmarshal(v, t) == nil && t.Agent == "" { // an agent's token reaches what its agent does
			end(bucketTokens, k, "token", t.Name, &t.MachineAccess, t)
		}
		return true
	})
	tx.Range(bucketAgents, kv.Range{}, func(k, v []byte) bool {
		a := new(Agent)
		if json.Unmarshal(v, a) == nil {
			end(bucketAgents, k, "agent", a.Name, &a.MachineAccess, a)
		}
		return true
	})
	tx.Range(bucketGrants, kv.Range{}, func(k, v []byte) bool {
		g := new(Grant)
		if json.Unmarshal(v, g) == nil {
			end(bucketGrants, k, "grant", g.ID, &g.MachineAccess, g)
		}
		return true
	})
	return puts, entries, tokens
}

// endCriticalNames gives the credentials that name machine, just marked critical, their ends, in tx,
// and reports whether a token changed.
func (s *Store) endCriticalNames(tx kv.Tx, raw *sql.Tx, machine string, now time.Time, actor, via string) (bool, error) {
	puts, entries, tokens := criticalEnds(tx, machine, now, actor, via)
	for _, p := range puts {
		b, err := json.Marshal(p.v)
		if err != nil {
			return false, err
		}
		if err := tx.Put(p.bucket, p.key, b); err != nil {
			return false, err
		}
	}
	for _, e := range entries {
		if err := s.audit.AppendTx(raw, e); err != nil {
			return false, err
		}
	}
	return tokens, nil
}

// NamedOn lists the tokens, agents and OAuth grants that name machine, with their access, by kind and
// name: for the chat when a machine becomes critical.
func (s *Store) NamedOn(machine string) []MachineAccessChange {
	var out []MachineAccessChange
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketTokens, kv.Range{}, func(_, v []byte) bool {
			var t Token
			if json.Unmarshal(v, &t) == nil && t.Agent == "" && t.Names(machine) {
				out = append(out, MachineAccessChange{Kind: "token", Name: t.Name, Access: t.MachineAccess})
			}
			return true
		})
		tx.Range(bucketAgents, kv.Range{}, func(_, v []byte) bool {
			var a Agent
			if json.Unmarshal(v, &a) == nil && a.Names(machine) {
				out = append(out, MachineAccessChange{Kind: "agent", Name: a.Name, Access: a.MachineAccess})
			}
			return true
		})
		tx.Range(bucketGrants, kv.Range{}, func(_, v []byte) bool {
			var g Grant
			if json.Unmarshal(v, &g) == nil && g.Names(machine) {
				out = append(out, MachineAccessChange{Kind: "grant", Name: g.ID, Access: g.MachineAccess})
			}
			return true
		})
		return nil
	})
	slices.SortFunc(out, func(a, b MachineAccessChange) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Name, b.Name))
	})
	return out
}
