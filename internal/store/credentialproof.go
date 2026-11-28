package store

import (
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

type proofKind uint8

const (
	proofToken proofKind = iota + 1
	proofAccess
	proofConsole
)

// credentialProof carries the credential that was presented, by its hash, without ever being
// serialised as principal metadata. Team credentials recheck this proof in their transaction.
type credentialProof struct {
	kind proofKind
	hash string
}

// InConsoleSession ties the current principal to the exact console session id that was presented.
func (p Principal) InConsoleSession(id string) Principal {
	if id != "" && p.Person() {
		p.proof = credentialProof{kind: proofConsole, hash: hashSession(id)}
	}
	return p
}

// TeamPrincipal revalidates an owner using an account session, or an agent using the exact OAuth
// access token or standalone agent token that authenticated the request. It reads identity and
// policy from the same database transaction that will record the operation.
func (s *Store) TeamPrincipal(tx kv.Tx, p Principal) (Principal, Agent, bool) {
	var current Principal
	now := s.now()
	switch p.proof.kind {
	case proofConsole:
		var cs ConsoleSession
		if p.User != "" && p.Agent == "" && decodeKey(tx, bucketConsole, []byte(p.proof.hash), &cs) &&
			cs.User == p.User && now.Before(cs.Expires) {
			if account, found := getAccount(tx, p.User); found && !account.Disabled {
				return personPrincipal(tx, account, now), Agent{}, true
			}
		}
		return Principal{}, Agent{}, false
	case proofToken:
		var token Token
		if p.Agent == "" || !decodeKey(tx, bucketTokens, []byte(p.Token), &token) ||
			token.Agent != p.Agent || !matchesTeamToken(token, p.proof.hash, now) {
			return Principal{}, Agent{}, false
		}
		current, _ = agentPrincipal(tx, token.Agent, now)
	case proofAccess:
		var a accessRecord
		if p.Agent == "" || !decodeKey(tx, bucketAccess, []byte(p.proof.hash), &a) || a.Grant != p.Grant || !now.Before(a.Expires) {
			return Principal{}, Agent{}, false
		}
		gs := grants(tx, func(g Grant) bool { return g.ID == a.Grant })
		if len(gs) != 1 || gs[0].Agent != p.Agent || !now.Before(gs[0].Expires) {
			return Principal{}, Agent{}, false
		}
		current, _ = grantPrincipal(tx, gs[0], now)
	default:
		return Principal{}, Agent{}, false
	}
	if current.Agent == "" || current.User != p.User {
		return Principal{}, Agent{}, false
	}
	agent, ok := getAgent(tx, current.Agent)
	return current, agent, ok
}

func matchesTeamToken(t Token, hash string, now time.Time) bool {
	return hash != "" && now.Before(t.Expires) && (hash == t.Hash || hash == t.OldHash && now.Before(t.OldUntil))
}

// TeamAgent finds a delegation's recipient and live owner from the current transaction.
func (s *Store) TeamAgent(tx kv.Tx, name string) (Principal, Agent, bool) {
	p, ok := agentPrincipal(tx, name, s.now())
	if !ok {
		return Principal{}, Agent{}, false
	}
	a, ok := getAgent(tx, name)
	return p, a, ok
}
