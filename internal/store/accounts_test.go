package store

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestMain makes password hashing cheap for the tests: the parameters are recorded in each hash, so the
// code under test is the same.
func TestMain(m *testing.M) {
	PasswordParams = Argon2Params{Time: 1, Memory: 64, Threads: 1}
	os.Exit(m.Run())
}

func openTest(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	var s *Store
	var err error
	if dsn := os.Getenv("BEACON_TEST_POSTGRES_DSN"); dsn != "" {
		s, err = OpenWith(Options{Driver: "postgres", DSN: testDatabase(t, dsn)})
	} else {
		s, err = Open(filepath.Join(t.TempDir(), "s.db"))
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}

// owner sets Beacon up with its first account.
func owner(t *testing.T, s *Store, name string) Account {
	t.Helper()
	code, _, err := s.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Join(code, name, "", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// invite adds a person with role.
func invite(t *testing.T, s *Store, name, role string) Account {
	t.Helper()
	code, _, err := s.CreateInvitation(role, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Join(code, name, "", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPasswords(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=64,t=1,p=1$") || strings.Contains(h, "correct") {
		t.Fatalf("hash %q %v", h, err)
	}
	if ok, stale := VerifyPassword(h, "correct horse battery"); !ok || stale {
		t.Errorf("verify: %v %v", ok, stale)
	}
	for _, wrong := range []string{"correct horse batter", "Correct horse battery", "", strings.Repeat("x", 2000)} {
		if ok, _ := VerifyPassword(h, wrong); ok {
			t.Errorf("%q verifies", wrong)
		}
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=64,t=1,p=1$c2FsdA$a2V5", "$argon2id$v=19$m=99999999,t=1,p=1$c2FsdA$a2V5"} {
		if ok, _ := VerifyPassword(bad, "x"); ok {
			t.Errorf("%q verifies", bad)
		}
	}
	old := PasswordParams
	PasswordParams = Argon2Params{Time: 2, Memory: 64, Threads: 1}
	defer func() { PasswordParams = old }()
	if ok, stale := VerifyPassword(h, "correct horse battery"); !ok || !stale {
		t.Errorf("a hash with other parameters: %v %v", ok, stale)
	}

	for pw, why := range map[string]string{
		"short":                 "shorter than 10",
		"alice":                 "shorter than 10",
		"alice.zhang":           "the username",
		"Alice.Zhang":           "the username",
		"Password123":           "commonest",
		"QWERTYUIOP":            "commonest",
		"1234567890":            "commonest",
		"ten chars!":            "",
		strings.Repeat("é", 10): "",
	} {
		err := CheckPassword("alice.zhang", pw)
		if why == "" && err != nil || why != "" && (err == nil || !strings.Contains(err.Error(), why)) {
			t.Errorf("%q: %v, want %q", pw, err, why)
		}
	}
}

func TestSetupAndInvitations(t *testing.T) {
	s, now := openTest(t)
	if s.HasAccounts() {
		t.Fatal("a new store has accounts")
	}
	code, l, err := s.CreateSetupLink()
	if err != nil || len(code) < 40 || l.Kind != LinkSetup || !l.Expires.Equal(now.Add(30*time.Minute)) || strings.Contains(l.Hash, code) {
		t.Fatalf("setup link: %q %+v %v", code, l, err)
	}
	second, _, _ := s.CreateSetupLink() // beacon setup on demand: another link, both valid
	if _, err := s.Link(LinkSetup, code); err != nil {
		t.Errorf("the first link: %v", err)
	}
	if _, err := s.Link(LinkInvite, code); !errors.Is(err, ErrBadLink) {
		t.Errorf("a setup link taken as an invitation: %v", err)
	}
	for _, bad := range []struct{ user, pw, want string }{
		{"A", "correct horse battery", "username"},
		{"beacon", "correct horse battery", "reserved"},
		{"alice", "short", "shorter"},
		{"alice", "password123", "commonest"},
	} {
		if _, err := s.Join(code, bad.user, "", bad.pw); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("join %q %q: %v", bad.user, bad.pw, err)
		}
	}
	a, err := s.Join(code, "alice", "Alice Zhang", "correct horse battery")
	if err != nil || a.Role != "owner" || a.DisplayName != "Alice Zhang" || a.CreatedBy != "setup" || strings.Contains(a.PasswordHash, "horse") {
		t.Fatalf("join: %+v %v", a, err)
	}
	// one-time: used, and no second owner by the other setup link either
	if _, err := s.Join(code, "mallory", "", "correct horse battery"); !errors.Is(err, ErrBadLink) {
		t.Errorf("the setup link twice: %v", err)
	}
	if _, err := s.Join(second, "mallory", "", "correct horse battery"); !errors.Is(err, ErrHasAccounts) {
		t.Errorf("a second setup link after setup: %v", err)
	}
	if _, _, err := s.CreateSetupLink(); !errors.Is(err, ErrHasAccounts) {
		t.Errorf("a setup link with accounts: %v", err)
	}

	inv, l, err := s.CreateInvitation("oncall", "Bob, on call from Monday", "alice")
	if err != nil || l.Role != "oncall" || !l.Expires.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("invitation: %+v %v", l, err)
	}
	if _, _, err := s.CreateInvitation("root", "", "alice"); err == nil {
		t.Error("an invitation with an unknown role")
	}
	if got := s.Invitations(); len(got) != 1 || got[0].Note != "Bob, on call from Monday" {
		t.Errorf("invitations: %+v", got)
	}
	if _, err := s.Join(inv, "alice", "", "another long password"); !errors.Is(err, ErrExists) {
		t.Errorf("a taken username: %v", err)
	}
	*now = now.Add(25 * time.Hour)
	if _, err := s.Join(inv, "bob", "", "another long password"); !errors.Is(err, ErrLinkExpired) || !errors.Is(err, ErrBadLink) {
		t.Errorf("an expired invitation: %v", err)
	}
	inv, l, _ = s.CreateInvitation("viewer", "", "alice")
	if _, err := s.WithdrawInvitation(l.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join(inv, "bob", "", "another long password"); !errors.Is(err, ErrBadLink) {
		t.Errorf("a withdrawn invitation: %v", err)
	}
	inv, _, _ = s.CreateInvitation("viewer", "", "alice")
	if b, err := s.Join(inv, "bob", "", "another long password"); err != nil || b.Role != "viewer" || b.CreatedBy != "alice" {
		t.Errorf("bob: %+v %v", b, err)
	}
}

func TestSignInAndAccounts(t *testing.T) {
	s, now := openTest(t)
	owner(t, s, "alice")
	invite(t, s, "bob", "oncall")

	if _, err := s.SignIn("bob", "wrong password!"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("a wrong password: %v", err)
	}
	if _, err := s.SignIn("nobody", "correct horse battery"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("an unknown user: %v", err)
	}
	*now = now.Add(time.Hour)
	b, err := s.SignIn("bob", "correct horse battery")
	if err != nil || !b.LastSignIn.Equal(*now) {
		t.Fatalf("sign in: %+v %v", b, err)
	}

	// a hash with old parameters is made again at sign-in
	old := PasswordParams
	PasswordParams = Argon2Params{Time: 2, Memory: 64, Threads: 1}
	if _, err := s.SignIn("bob", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account("bob"); !strings.Contains(a.PasswordHash, "t=2") {
		t.Errorf("not rehashed: %s", a.PasswordHash)
	}
	PasswordParams = old

	// sessions end when the account is disabled or its password changes
	id1, _, _ := s.CreateAccountSession(b, 12*time.Hour)
	id2, _, _ := s.CreateAccountSession(b, 12*time.Hour)
	if err := s.ChangePassword("bob", "wrong", "a brand new password", id1); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("change without the current password: %v", err)
	}
	if err := s.ChangePassword("bob", "correct horse battery", "bob", id1); err == nil {
		t.Error("the username as the password")
	}
	if err := s.ChangePassword("bob", "correct horse battery", "a brand new password", id1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsoleSessionByID(id1); err != nil {
		t.Error("the session that changed the password ended")
	}
	if _, err := s.ConsoleSessionByID(id2); err == nil {
		t.Error("another session outlived the password change")
	}
	if _, err := s.SignIn("bob", "a brand new password"); err != nil {
		t.Errorf("the new password: %v", err)
	}
	if _, err := s.UpdateAccount("bob", func(a *Account) error { a.Disabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsoleSessionByID(id1); err == nil {
		t.Error("a session outlived disabling the account")
	}
	if _, err := s.SignIn("bob", "a brand new password"); !errors.Is(err, ErrDisabled) {
		t.Errorf("a disabled account: %v", err)
	}

	// the last owner stays
	if _, err := s.UpdateAccount("alice", func(a *Account) error { a.Role = "operator"; return nil }); !errors.Is(err, ErrLastOwner) {
		t.Errorf("demoting the last owner: %v", err)
	}
	if _, err := s.UpdateAccount("alice", func(a *Account) error { a.Disabled = true; return nil }); !errors.Is(err, ErrLastOwner) {
		t.Errorf("disabling the last owner: %v", err)
	}
	if _, err := s.UpdateAccount("alice", func(a *Account) error { a.Role = "god"; return nil }); err == nil {
		t.Error("an unknown role")
	}
	if a, err := s.UpdateAccount("alice", func(a *Account) error { a.Username, a.PasswordHash = "eve", "x"; return nil }); err != nil || a.Username != "alice" || a.PasswordHash == "x" {
		t.Errorf("username and password changed through UpdateAccount: %+v %v", a, err)
	}

	// a reset link: one-time, 30 minutes, ends the sessions
	code, l, err := s.CreateResetLink("bob", "alice")
	if err != nil || l.User != "bob" || !l.Expires.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("reset link: %+v %v", l, err)
	}
	if _, _, err := s.CreateResetLink("nobody", "alice"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a reset link for nobody: %v", err)
	}
	if _, err := s.Reset(code, "short"); err == nil {
		t.Error("a short password through a reset")
	}
	if _, err := s.Reset(code, "reset to this one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reset(code, "reset to this one again"); !errors.Is(err, ErrBadLink) {
		t.Errorf("a reset link twice: %v", err)
	}
	code, _, _ = s.CreateResetLink("alice", "alice")
	*now = now.Add(31 * time.Minute)
	if _, err := s.Reset(code, "too late for this"); !errors.Is(err, ErrLinkExpired) || !errors.Is(err, ErrBadLink) {
		t.Errorf("an expired reset link: %v", err)
	}
	if _, err := s.Link(LinkReset, code); !errors.Is(err, ErrLinkExpired) {
		t.Errorf("an expired link through Link: %v", err)
	}
}

// TestExpiredLinkOutlivesANewerOne keeps a link that has only just expired when a newer one is made, and
// forgets it once it has been expired for more than a week.
func TestExpiredLinkOutlivesANewerOne(t *testing.T) {
	s, now := openTest(t)
	old, _, err := s.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(31 * time.Minute)
	fresh, _, err := s.CreateSetupLink()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Link(LinkSetup, old); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("an expired link after a newer one: %v", err)
	}
	if _, err := s.Link(LinkSetup, fresh); err != nil {
		t.Fatalf("the newer link: %v", err)
	}
	*now = now.Add(8 * 24 * time.Hour)
	if _, _, err := s.CreateSetupLink(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Link(LinkSetup, old); errors.Is(err, ErrLinkExpired) || !errors.Is(err, ErrBadLink) {
		t.Fatalf("a link expired for over a week: %v", err)
	}
}

func TestRoles(t *testing.T) {
	if got := RoleScopes("viewer"); !slices.Equal(got, []string{"status:read", "sessions:read"}) {
		t.Errorf("viewer: %v", got)
	}
	if got := RoleScopes("oncall"); !slices.Contains(got, "machines:ssh") || slices.Contains(got, "machines:write") {
		t.Errorf("oncall: %v", got)
	}
	if got := RoleScopes("owner"); !slices.Contains(got, "accounts:write") || !slices.Contains(got, "config:write") || len(got) != len(PeopleScopes) {
		t.Errorf("owner: %v", got)
	}
	if RoleScopes("root") != nil {
		t.Error("an unknown role has scopes")
	}
	for _, sc := range PeopleScopes {
		if !slices.Contains(Scopes, sc) {
			t.Errorf("%s is not a scope", sc)
		}
	}
	if !PresetWithin("oncall", "operator") || PresetWithin("operator", "oncall") || !PresetWithin("operator", "owner") || PresetWithin("custom", "owner") {
		t.Error("PresetWithin")
	}
	if ClosestPreset([]string{"sessions:read", "status:read"}) != "readonly" || ClosestPreset([]string{"status:read", "alerts:write", "machines:ssh"}) != PresetCustom {
		t.Error("ClosestPreset")
	}
	for in, want := range map[string]string{"Claude Code": "claude-code", "Codex CLI 1.0": "codex-cli-1-0", "": "agent", "  !!": "agent", "ÉÉ": "agent"} {
		if got := AgentName(in); got != want || ValidAgentName(got) != nil {
			t.Errorf("AgentName(%q) = %q", in, got)
		}
	}
}
