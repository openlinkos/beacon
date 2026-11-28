package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

// Changes that give access are written together with their audit entries, in one transaction: when an
// entry cannot be written, the change is not made either (ErrUnrecorded). A change that takes access
// away is not held to its record: its caller writes the entry afterwards (Audit), and a failure there is
// reported (OnAuditFailure) without stopping it.

// ErrUnrecorded means a change was not made because the audit entry written with it could not be.
var ErrUnrecorded = errors.New("the audit log cannot be written now, so nothing was changed; try again once it can")

// Record gives the audit entries a change writes in its own transaction, from what was there before and
// what the change makes: none when the change gives nothing new (its caller records it afterwards).
type Record[T any] func(before, after T) []AuditEntry

// appendRecords writes entries in raw, the transaction of the change they record. A failure is reported
// like any audit failure, and fails the change (ErrUnrecorded).
func (s *Store) appendRecords(raw *sql.Tx, entries []AuditEntry) error {
	for _, e := range entries {
		if e.Time.IsZero() {
			e.Time = s.now().UTC()
		}
		ev := events.Event{Time: e.Time, Actor: e.Actor, Via: e.Via, Action: e.Action, Object: e.Target, Detail: e.Detail}
		if err := s.audit.AppendTx(raw, &ev); err != nil {
			s.reportAuditFailure(e, err)
			return fmt.Errorf("%w (%s %s): %v", ErrUnrecorded, e.Action, e.Target, err) //nolint:errorlint // one wrapped error is enough
		}
	}
	return nil
}

// UpdateAccountRecorded is UpdateAccount, writing what record gives in the same transaction.
func (s *Store) UpdateAccountRecorded(username string, change func(*Account) error, record Record[Account]) (Account, error) {
	return s.updateAccount(username, change, record)
}

// SetCriticalMachinesRecorded is SetCriticalMachines, writing what record gives in the same transaction.
func (s *Store) SetCriticalMachinesRecorded(username string, named MachineAccess, record Record[Account]) (before, after Account, err error) {
	named = named.normal()
	if err := named.check(); err != nil {
		return Account{}, Account{}, err
	}
	after, err = s.updateAccount(username, func(a *Account) error {
		before = *a
		a.CriticalMachines, a.CriticalUntil = named.Machines, named.MachineUntil
		return nil
	}, record)
	return before, after, err
}

// UpdateAgentRecorded is UpdateAgent, writing what record gives in the same transaction.
func (s *Store) UpdateAgentRecorded(name string, change func(*Agent) error, record Record[Agent]) (Agent, error) {
	return s.updateAgent(name, change, record)
}

// UpdateTokenRecorded is UpdateToken, writing what record gives in the same transaction.
func (s *Store) UpdateTokenRecorded(name string, change func(*Token) error, record Record[Token]) (Token, error) {
	return s.updateTokenRecorded(name, func(_ kv.Tx, t *Token) error { return change(t) }, record)
}

// AddTokenSSHKeyRecorded is AddTokenSSHKey, writing what record gives in the same transaction.
func (s *Store) AddTokenSSHKeyRecorded(name string, k SSHKey, record Record[Token]) (Token, error) {
	return s.updateTokenRecorded(name, addTokenKey(name, k), record)
}

// CreateInvitationRecorded is CreateInvitation, writing what record gives for the new link in the same
// transaction.
func (s *Store) CreateInvitationRecorded(role, note string, by LinkMaker, record func(AccountLink) []AuditEntry) (string, AccountLink, error) {
	l, err := invitation(role, note, by)
	if err != nil {
		return "", AccountLink{}, err
	}
	return s.createLink(l, InviteTTL, nil, record)
}

// CreateResetLinkRecorded is CreateResetLink, writing what record gives for the new link in the same
// transaction.
func (s *Store) CreateResetLinkRecorded(username string, by LinkMaker, record func(AccountLink) []AuditEntry) (string, AccountLink, error) {
	if by.Key == "" {
		return "", AccountLink{}, errors.New("a link names who made it")
	}
	return s.createLink(AccountLink{Kind: LinkReset, User: username, CreatedBy: by.Name, Maker: by.Key}, ResetTTL, resetCheck(username), record)
}

// CreateSubjectTokenRecorded is CreateSubjectToken, writing what record gives for the new token in the
// same transaction. With replace, a token already called name is replaced in that transaction, with
// the links it made and the keys added with it: should the record fail, the old token keeps working.
// record is given the token replaced (nil when there was none) and the new one.
func (s *Store) CreateSubjectTokenRecorded(name, subject string, scopes []string, ttl time.Duration, by string, replace bool,
	record func(old *Token, t Token) []AuditEntry) (string, Token, error) {
	return s.createToken(name, subject, scopes, ttl, by, replace, record)
}

// RegisterMachineRecorded is RegisterMachine, writing what record gives in the same transaction: given
// the registration replaced (the zero Machine for a new one) and the new one.
func (s *Store) RegisterMachineRecorded(m Machine, previous *Machine, via string, record Record[Machine]) (Machine, error) {
	return s.registerMachine(m, previous, via, record)
}

// UpdateMachineIdentityRecorded is UpdateMachineIdentity, writing what record gives in the same
// transaction.
func (s *Store) UpdateMachineIdentityRecorded(name string, same func(Machine) bool, change func(*Machine), record Record[Machine]) (Machine, error) {
	return s.updateMachineIdentity(name, same, change, record)
}

// JoinRecorded is Join, writing what record gives for the new account in the same transaction: should
// the record fail, no account is made and the link still works.
func (s *Store) JoinRecorded(code, username, displayName, password string, record func(Account) []AuditEntry) (Account, error) {
	return s.join(code, username, displayName, password, record)
}

// RenewTokenRecorded is RenewToken, writing what record gives for the renewed token in the same
// transaction: should the record fail, the token keeps the secret it had.
func (s *Store) RenewTokenRecorded(name string, record func(Token) []AuditEntry) (string, Token, error) {
	return s.renewToken(name, record)
}

// CreateAgentRecorded is CreateAgent for an agent that holds keys for the SSH channel from the start,
// each under AddAgentSSHKey's rule, writing what record gives for it in the same transaction.
func (s *Store) CreateAgentRecorded(a Agent, keys []SSHKey, record func(Agent) []AuditEntry) (Agent, error) {
	return s.createAgent(a, keys, record)
}

// CreateAgentTokenRecorded is CreateAgentToken, writing what record gives for the new token in the same
// transaction.
func (s *Store) CreateAgentTokenRecorded(agent string, ttl time.Duration, by string, record func(Token) []AuditEntry) (string, Token, error) {
	return s.createAgentToken(agent, ttl, by, record)
}

// AddAccountSSHKeyRecorded is AddAccountSSHKey, writing what record gives in the same transaction.
func (s *Store) AddAccountSSHKeyRecorded(username string, k SSHKey, record Record[Account]) error {
	return s.addAccountSSHKey(username, k, record)
}

// AddAgentSSHKeyRecorded is AddAgentSSHKey, writing what record gives in the same transaction.
func (s *Store) AddAgentSSHKeyRecorded(agent string, k SSHKey, record Record[Agent]) (Agent, error) {
	return s.addAgentSSHKey(agent, k, record)
}

// grantedAccess reports whether after reaches a machine before did not, or reaches one for longer.
func grantedAccess(before, after MachineAccess, critical func(string) bool, now time.Time) bool {
	return !after.Live(now).Within(before.Live(now), critical)
}

// TokenAccessWidens reports whether a token's machine access after a change reaches something it did
// not before: all machines, a machine it did not reach, or one for longer. critical says which machines
// are critical now.
func TokenAccessWidens(before, after MachineAccess, critical map[string]bool, now time.Time) bool {
	return grantedAccess(before, after, func(m string) bool { return critical[m] }, now)
}

// CriticalNamesWiden reports whether the critical machines named on an account after a change include a
// machine not named before, or one for longer.
func CriticalNamesWiden(before, after Account, now time.Time) bool {
	return grantedAccess(before.CriticalAccess(), after.CriticalAccess(), func(string) bool { return true }, now)
}

// RoleRaised reports whether role gives a scope that before does not.
func RoleRaised(before, after string) bool {
	given := RoleScopes(before)
	for _, sc := range RoleScopes(after) {
		if !slices.Contains(given, sc) {
			return true
		}
	}
	return false
}
