package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/openlinkos/beacon/internal/kv"
)

// People's accounts: a username and a password, a role that says what they may do, and the agents they
// own (principals.go). Beacon signs people in itself and nothing else does: no external sign-in of any
// kind. The first account, an owner, is made with a one-time setup link while there is none; the rest
// are invited by an owner with one-time links; a forgotten password is reset with a one-time link an
// owner makes. Only an argon2id hash of a password is kept, and only the SHA-256 of a link's code.

var (
	bucketAccounts = []byte("accounts")
	bucketLinks    = []byte("account_links")
)

// Roles are what a person may be, weakest first. Each gives the scopes of the one before and more.
var Roles = []string{"viewer", "oncall", "operator", "owner"}

// roleAdds is what each role adds to the one before it.
var roleAdds = map[string][]string{
	"viewer":   {"status:read", "sessions:read", "secrets:describe"},
	"oncall":   {"alerts:write", "machines:ssh", "secrets:read", "secrets:use"},
	"operator": {"maintenance:write", "incidents:write", "machines:write", "secrets:review"},
	"owner":    {"config:write", "accounts:write", "secrets:admin"},
}

// RoleScopes are the scopes a role gives; none for an unknown role.
func RoleScopes(role string) []string {
	var out []string
	for _, r := range Roles {
		out = append(out, roleAdds[r]...)
		if r == role {
			return out
		}
	}
	return nil
}

// ValidRole reports whether role is one of Roles.
func ValidRole(role string) bool { return slices.Contains(Roles, role) }

// Account is a person. Username is how they sign in and how the audit log names them; it never changes.
// PasswordHash is the argon2id hash of their password in the PHC string form. FeishuOpenID is a contact
// field for the cards, never used to sign in.
type Account struct {
	Username        string    `json:"username"`
	DisplayName     string    `json:"display_name"`
	Role            string    `json:"role"`
	PasswordHash    string    `json:"password_hash"`
	FeishuOpenID    string    `json:"feishu_open_id,omitempty"`
	Created         time.Time `json:"created"`
	CreatedBy       string    `json:"created_by,omitempty"`
	LastSignIn      time.Time `json:"last_sign_in,omitzero"`
	PasswordChanged time.Time `json:"password_changed,omitzero"`
	Disabled        bool      `json:"disabled,omitempty"`
}

// Account errors.
var (
	// ErrBadCredentials is the one answer to a wrong username or password, so that a sign-in does not
	// tell which accounts exist.
	ErrBadCredentials = errors.New("wrong username or password")
	// ErrDisabled means the account is disabled.
	ErrDisabled = errors.New("the account is disabled")
	// ErrBadLink means a setup, invitation or reset link is unknown, used or expired.
	ErrBadLink = errors.New("the link is unknown, used already or expired")
	// ErrLinkExpired names the one case ErrBadLink can prove: the link's time is up. It wraps ErrBadLink,
	// so an expired link still counts as a bad one wherever that matters.
	ErrLinkExpired = fmt.Errorf("the link has expired: %w", ErrBadLink)
	// ErrExists means the name is taken.
	ErrExists = errors.New("the name is taken")
	// ErrHasAccounts means Beacon has accounts already, so there is nothing to set up.
	ErrHasAccounts = errors.New("there are accounts already; an owner invites people from the console or with beacon user invite")
	// ErrLastOwner means the change would leave Beacon without an enabled owner.
	ErrLastOwner = errors.New("that would leave Beacon without an owner; make someone else owner first")

	// What is wrong with a username, a display name or a password.
	ErrBadUsername      = errors.New("use 2 to 32 lower-case letters, digits, dots, dashes and underscores, starting with a letter or a digit")
	ErrReservedUsername = errors.New("the username is reserved; choose another")
	ErrBadDisplayName   = errors.New("the display name is at most 64 characters, without control characters")
	ErrPasswordShort    = errors.New("the password is shorter than 10 characters")
	ErrPasswordLong     = errors.New("the password is longer than 1024 bytes")
	ErrPasswordUsername = errors.New("the password is the username")
	ErrPasswordCommon   = errors.New("the password is one of the commonest; choose another")
)

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

// reserved are names that would be mistaken for Beacon's own actors in the audit log.
var reserved = []string{"beacon", "unknown", "rules", "root"}

// ValidUsername checks a username: 2 to 32 lower-case letters, digits, dots, dashes and underscores,
// starting with a letter or a digit.
func ValidUsername(name string) error {
	if !usernameRe.MatchString(name) {
		return fmt.Errorf("username %q: %w", name, ErrBadUsername)
	}
	if slices.Contains(reserved, name) {
		return fmt.Errorf("username %q: %w", name, ErrReservedUsername)
	}
	return nil
}

// cleanDisplayName trims a display name and checks it: 1 to 64 characters without control characters.
func cleanDisplayName(name, username string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return username, nil
	}
	if utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, unicode.IsControl) || !utf8.ValidString(name) {
		return "", ErrBadDisplayName
	}
	return name, nil
}

// ---- passwords

// MinPassword is the shortest password Beacon takes.
const MinPassword = 10

// maxPassword bounds what is hashed.
const maxPassword = 1024

// common are the commonest passwords of 10 characters or more, refused whatever the case.
var common = []string{
	"1234567890", "12345678910", "123456789a", "0123456789", "0987654321", "1111111111", "1q2w3e4r5t", "1qaz2wsx3edc",
	"password1", "password12", "password123", "password1234", "passw0rd123", "p@ssw0rd123", "qwertyuiop", "qwerty1234",
	"qwerty12345", "qwerty123456", "asdfghjkl1", "zxcvbnm123", "iloveyou12", "letmein123", "welcome123", "welcome1234",
	"administrator", "admin12345", "admin123456", "changeme123", "abcdefghij", "abc1234567", "a123456789", "aa12345678",
	"superman123", "football123", "baseball123", "princess123", "sunshine123", "monkey12345", "dragon12345", "master12345",
	"beacon12345", "beacon123456", "beaconbeacon",
}

// CheckPassword says what is wrong with a password for username, or nil: at least MinPassword
// characters, not the username, not one of the commonest passwords. No other rules.
func CheckPassword(username, password string) error {
	switch {
	case utf8.RuneCountInString(password) < MinPassword:
		return ErrPasswordShort
	case len(password) > maxPassword:
		return ErrPasswordLong
	case strings.EqualFold(password, username) || strings.EqualFold(strings.TrimSpace(password), username):
		return ErrPasswordUsername
	case slices.Contains(common, strings.ToLower(password)):
		return ErrPasswordCommon
	}
	return nil
}

// Argon2Params are the argon2id parameters of a password hash.
type Argon2Params struct {
	Time    uint32 // passes
	Memory  uint32 // KiB
	Threads uint8
}

// PasswordParams are the parameters new password hashes are made with: RFC 9106's second recommended
// option (64 MiB, 3 passes, 4 lanes). A hash made with others is made again at the next sign-in. Tests
// lower them.
var PasswordParams = Argon2Params{Time: 3, Memory: 64 * 1024, Threads: 4}

const (
	saltLen = 16
	keyLen  = 32
)

// hashing bounds how many passwords are hashed at once, and so the memory it takes.
var hashing = make(chan struct{}, 2)

func argon(password string, salt []byte, p Argon2Params) []byte {
	hashing <- struct{}{}
	defer func() { <-hashing }()
	return argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyLen)
}

// HashPassword hashes a password with PasswordParams, in the PHC string form.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := PasswordParams
	key := argon(password, salt, p)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// parseHash reads a PHC string made by HashPassword.
func parseHash(h string) (Argon2Params, []byte, []byte, error) {
	var p Argon2Params
	parts := strings.Split(h, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return p, nil, nil, errors.New("not an argon2id hash")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil || p.Memory == 0 || p.Time == 0 || p.Threads == 0 ||
		p.Memory > 1<<20 || p.Time > 64 || p.Threads > 64 {
		return p, nil, nil, errors.New("bad argon2id parameters")
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	key, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(key) == 0 {
		return p, nil, nil, errors.New("bad argon2id hash")
	}
	return p, salt, key, nil
}

// VerifyPassword reports whether password matches the hash, in constant time, and whether the hash was
// made with other parameters than PasswordParams (then it should be made again).
func VerifyPassword(hash, password string) (ok, stale bool) {
	p, salt, key, err := parseHash(hash)
	if err != nil || len(password) > maxPassword {
		return false, false
	}
	if len(key) != keyLen {
		return false, false
	}
	got := argon(password, salt, p)
	if subtle.ConstantTimeCompare(got, key) != 1 {
		return false, false
	}
	return true, p != PasswordParams
}

// dummyHash is checked against when a username is unknown, so that a sign-in takes as long either way.
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("not anyone's password, ever")
	return h
})

// ---- accounts

// HasAccounts reports whether Beacon has any account yet.
func (s *Store) HasAccounts() bool {
	n := 0
	_ = s.db.View(func(tx kv.Tx) error {
		n = tx.Count(bucketAccounts)
		return nil
	})
	return n > 0
}

// Account returns the account called username.
func (s *Store) Account(username string) (Account, bool) {
	var a Account
	ok := false
	_ = s.db.View(func(tx kv.Tx) error {
		if v := tx.Get(bucketAccounts, []byte(username)); v != nil {
			ok = json.Unmarshal(v, &a) == nil
		}
		return nil
	})
	return a, ok
}

// Accounts lists the accounts by username.
func (s *Store) Accounts() []Account {
	var out []Account
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketAccounts, kv.Range{}, func(_, v []byte) bool {
			var a Account
			if json.Unmarshal(v, &a) == nil {
				out = append(out, a)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

func putAccount(tx kv.Tx, a Account) error {
	v, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return tx.Put(bucketAccounts, []byte(a.Username), v)
}

func getAccount(tx kv.Tx, username string) (Account, bool) {
	var a Account
	v := tx.Get(bucketAccounts, []byte(username))
	return a, v != nil && json.Unmarshal(v, &a) == nil
}

// owners counts the enabled owners in tx, leaving out one username.
func owners(tx kv.Tx, except string) int {
	n := 0
	tx.Range(bucketAccounts, kv.Range{}, func(k, v []byte) bool {
		var a Account
		if string(k) != except && json.Unmarshal(v, &a) == nil && a.Role == "owner" && !a.Disabled {
			n++
		}
		return true
	})
	return n
}

// UpdateAccount changes the account called username with change, which cannot alter its username or
// password (SetPassword does). A change that would leave no enabled owner is refused. A role lowered or
// an account disabled limits its sessions and its agents at once: they are read afresh on every request.
func (s *Store) UpdateAccount(username string, change func(*Account) error) (Account, error) {
	var out Account
	err := s.db.Update(func(tx kv.Tx) error {
		a, ok := getAccount(tx, username)
		if !ok {
			return ErrNotFound
		}
		hash, created := a.PasswordHash, a.Created
		if err := change(&a); err != nil {
			return err
		}
		if !ValidRole(a.Role) {
			return fmt.Errorf("unknown role %q; roles are %s", a.Role, strings.Join(Roles, ", "))
		}
		dn, err := cleanDisplayName(a.DisplayName, username)
		if err != nil {
			return err
		}
		a.Username, a.PasswordHash, a.Created, a.DisplayName = username, hash, created, dn
		if (a.Role != "owner" || a.Disabled) && owners(tx, username) == 0 {
			if prev, _ := getAccount(tx, username); prev.Role == "owner" && !prev.Disabled {
				return ErrLastOwner
			}
		}
		out = a
		return putAccount(tx, a)
	})
	if err != nil {
		return Account{}, err
	}
	if out.Disabled {
		s.DeleteAccountSessions(username, "")
	}
	return out, nil
}

// SignIn checks a username and a password and records the sign-in. A wrong username or password is
// ErrBadCredentials either way, and takes as long; a disabled account is ErrDisabled, once the password
// is right. A hash made with older parameters is made again.
func (s *Store) SignIn(username, password string) (Account, error) {
	a, ok := s.Account(username)
	if !ok {
		VerifyPassword(dummyHash(), password)
		return Account{}, ErrBadCredentials
	}
	good, stale := VerifyPassword(a.PasswordHash, password)
	if !good {
		return Account{}, ErrBadCredentials
	}
	if a.Disabled {
		return Account{}, ErrDisabled
	}
	var newHash string
	if stale {
		newHash, _ = HashPassword(password)
	}
	now := s.now().UTC()
	err := s.db.Update(func(tx kv.Tx) error {
		cur, ok := getAccount(tx, username)
		if !ok {
			return ErrBadCredentials
		}
		if cur.PasswordHash != a.PasswordHash { // changed meanwhile: judge by the new one next time
			return ErrBadCredentials
		}
		cur.LastSignIn = now
		if newHash != "" {
			cur.PasswordHash = newHash
		}
		a = cur
		return putAccount(tx, cur)
	})
	return a, err
}

// SetPassword gives the account a new password (checked with CheckPassword) and ends its console
// sessions but the one called keep (the session that changed it; "" ends them all).
func (s *Store) SetPassword(username, password, keep string) error {
	if err := CheckPassword(username, password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	err = s.db.Update(func(tx kv.Tx) error {
		a, ok := getAccount(tx, username)
		if !ok {
			return ErrNotFound
		}
		a.PasswordHash, a.PasswordChanged = hash, s.now().UTC()
		return putAccount(tx, a)
	})
	if err != nil {
		return err
	}
	s.DeleteAccountSessions(username, keep)
	return nil
}

// ChangePassword is SetPassword for a person who knows the current password.
func (s *Store) ChangePassword(username, current, password, keep string) error {
	a, ok := s.Account(username)
	if !ok {
		return ErrNotFound
	}
	if good, _ := VerifyPassword(a.PasswordHash, current); !good {
		return ErrBadCredentials
	}
	return s.SetPassword(username, password, keep)
}

// ---- setup, invitation and reset links

// Link kinds and how long each lasts.
const (
	LinkSetup  = "setup"
	LinkInvite = "invite"
	LinkReset  = "reset"

	SetupTTL  = 30 * time.Minute
	InviteTTL = 24 * time.Hour
	ResetTTL  = 30 * time.Minute

	// expiredLinkKeep is how long an expired link stays. Opening it then still says it expired.
	// Forgetting it at once made a newer link (beacon setup, another invitation) turn the old page
	// into "used already", which it was not. A week covers a person coming back to the old tab.
	expiredLinkKeep = 7 * 24 * time.Hour
)

// AccountLink is a one-time link: to set up the first account (setup), to join with a role (invite), or
// to choose a new password (reset, for User). Only the SHA-256 of its code is kept; using it deletes it.
type AccountLink struct {
	Hash      string    `json:"hash"`
	Kind      string    `json:"kind"`
	Role      string    `json:"role,omitempty"`
	Note      string    `json:"note,omitempty"`
	User      string    `json:"user,omitempty"`
	Created   time.Time `json:"created"`
	CreatedBy string    `json:"created_by,omitempty"`
	Expires   time.Time `json:"expires"`
}

// ID names a pending link without its code: the start of its hash, for withdrawing an invitation.
func (l AccountLink) ID() string { return l.Hash[:16] }

func hashLink(code string) string {
	sum := sha256.Sum256([]byte("link:" + code))
	return hex.EncodeToString(sum[:])
}

// createLink stores a link and returns its code, which is not stored. A link expired longer than
// expiredLinkKeep is forgotten on the way, and so is a row that no longer reads. One that has only just
// expired stays, so opening it still says it expired after a newer link was made.
func (s *Store) createLink(l AccountLink, ttl time.Duration, check func(tx kv.Tx) error) (string, AccountLink, error) {
	code, err := randomID(32)
	if err != nil {
		return "", AccountLink{}, err
	}
	now := s.now().UTC()
	l.Hash, l.Created, l.Expires = hashLink(code), now, now.Add(ttl)
	err = s.db.Update(func(tx kv.Tx) error {
		if check != nil {
			if err := check(tx); err != nil {
				return err
			}
		}
		var old [][]byte
		tx.Range(bucketLinks, kv.Range{}, func(k, v []byte) bool {
			var x AccountLink
			if json.Unmarshal(v, &x) != nil || now.After(x.Expires.Add(expiredLinkKeep)) {
				old = append(old, append([]byte(nil), k...))
			}
			return true
		})
		for _, k := range old {
			_ = tx.Delete(bucketLinks, k)
		}
		v, _ := json.Marshal(l)
		return tx.Put(bucketLinks, []byte(l.Hash), v)
	})
	if err != nil {
		return "", AccountLink{}, err
	}
	return code, l, nil
}

// CreateSetupLink makes the link that creates the first account, an owner; ErrHasAccounts once one
// exists.
func (s *Store) CreateSetupLink() (string, AccountLink, error) {
	return s.createLink(AccountLink{Kind: LinkSetup, Role: "owner", CreatedBy: "beacon"}, SetupTTL, func(tx kv.Tx) error {
		if tx.Count(bucketAccounts) > 0 {
			return ErrHasAccounts
		}
		return nil
	})
}

// CreateInvitation makes a link with which someone joins with role.
func (s *Store) CreateInvitation(role, note, by string) (string, AccountLink, error) {
	if !ValidRole(role) {
		return "", AccountLink{}, fmt.Errorf("unknown role %q; roles are %s", role, strings.Join(Roles, ", "))
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 200 || strings.ContainsFunc(note, unicode.IsControl) {
		return "", AccountLink{}, errors.New("the note is at most 200 characters on one line")
	}
	return s.createLink(AccountLink{Kind: LinkInvite, Role: role, Note: note, CreatedBy: by}, InviteTTL, nil)
}

// CreateResetLink makes a link with which username chooses a new password.
func (s *Store) CreateResetLink(username, by string) (string, AccountLink, error) {
	return s.createLink(AccountLink{Kind: LinkReset, User: username, CreatedBy: by}, ResetTTL, func(tx kv.Tx) error {
		if _, ok := getAccount(tx, username); !ok {
			return ErrNotFound
		}
		return nil
	})
}

// Link returns the link whose code this is, if it is of kind and has not been used or expired.
func (s *Store) Link(kind, code string) (AccountLink, error) {
	var l AccountLink
	found := false
	_ = s.db.View(func(tx kv.Tx) error {
		l, found = getLink(tx, code)
		return nil
	})
	if !found || l.Kind != kind {
		return AccountLink{}, ErrBadLink
	}
	if !s.now().Before(l.Expires) {
		return AccountLink{}, ErrLinkExpired
	}
	return l, nil
}

func getLink(tx kv.Tx, code string) (AccountLink, bool) {
	var l AccountLink
	if code == "" {
		return l, false
	}
	v := tx.Get(bucketLinks, []byte(hashLink(code)))
	return l, v != nil && json.Unmarshal(v, &l) == nil
}

// Invitations lists the invitations not yet used or expired, the newest first.
func (s *Store) Invitations() []AccountLink {
	var out []AccountLink
	now := s.now()
	_ = s.db.View(func(tx kv.Tx) error {
		tx.Range(bucketLinks, kv.Range{}, func(_, v []byte) bool {
			var l AccountLink
			if json.Unmarshal(v, &l) == nil && l.Kind == LinkInvite && now.Before(l.Expires) {
				out = append(out, l)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// WithdrawInvitation deletes the pending invitation with this ID (AccountLink.ID).
func (s *Store) WithdrawInvitation(id string) (AccountLink, error) {
	var gone AccountLink
	err := s.db.Update(func(tx kv.Tx) error {
		if len(id) != 16 {
			return ErrNotFound
		}
		var key []byte
		tx.Range(bucketLinks, kv.Range{}, func(k, v []byte) bool {
			var l AccountLink
			if json.Unmarshal(v, &l) == nil && l.Kind == LinkInvite && strings.HasPrefix(l.Hash, id) {
				key, gone = append([]byte(nil), k...), l
			}
			return true
		})
		if key == nil {
			return ErrNotFound
		}
		return tx.Delete(bucketLinks, key)
	})
	return gone, err
}

// Join creates an account with a setup or invitation link, which it uses up: the first owner with the
// setup link (only while there is no account), or someone with the role an invitation names.
func (s *Store) Join(code, username, displayName, password string) (Account, error) {
	if err := ValidUsername(username); err != nil {
		return Account{}, err
	}
	dn, err := cleanDisplayName(displayName, username)
	if err != nil {
		return Account{}, err
	}
	if err := CheckPassword(username, password); err != nil {
		return Account{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Account{}, err
	}
	now := s.now().UTC()
	var a Account
	err = s.db.Update(func(tx kv.Tx) error {
		l, ok := getLink(tx, code)
		if !ok || (l.Kind != LinkSetup && l.Kind != LinkInvite) {
			return ErrBadLink
		}
		if !now.Before(l.Expires) {
			return ErrLinkExpired
		}
		if l.Kind == LinkSetup {
			if tx.Count(bucketAccounts) > 0 {
				return ErrHasAccounts
			}
		}
		if _, taken := getAccount(tx, username); taken {
			return fmt.Errorf("%w: someone has the username %q; choose another", ErrExists, username)
		}
		by := l.CreatedBy
		if l.Kind == LinkSetup {
			by = "setup"
		}
		a = Account{Username: username, DisplayName: dn, Role: l.Role, PasswordHash: hash, Created: now, CreatedBy: by, LastSignIn: now}
		if err := putAccount(tx, a); err != nil {
			return err
		}
		return tx.Delete(bucketLinks, []byte(l.Hash))
	})
	if err != nil {
		return Account{}, err
	}
	return a, nil
}

// Reset gives an account a new password with a reset link, which it uses up, and ends the account's
// console sessions.
func (s *Store) Reset(code, password string) (Account, error) {
	l, err := s.Link(LinkReset, code)
	if err != nil {
		return Account{}, err
	}
	if err := CheckPassword(l.User, password); err != nil {
		return Account{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Account{}, err
	}
	now := s.now().UTC()
	var a Account
	err = s.db.Update(func(tx kv.Tx) error {
		cur, ok := getLink(tx, code)
		if !ok || cur.Kind != LinkReset {
			return ErrBadLink
		}
		if !now.Before(cur.Expires) {
			return ErrLinkExpired
		}
		acc, ok := getAccount(tx, cur.User)
		if !ok {
			return ErrBadLink
		}
		acc.PasswordHash, acc.PasswordChanged = hash, now
		a = acc
		if err := putAccount(tx, acc); err != nil {
			return err
		}
		return tx.Delete(bucketLinks, []byte(cur.Hash))
	})
	if err != nil {
		return Account{}, err
	}
	s.DeleteAccountSessions(a.Username, "")
	return a, nil
}
