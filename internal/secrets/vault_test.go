package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/settings"
	"github.com/openlinkos/beacon/internal/signedhead"
	"github.com/openlinkos/beacon/internal/store"
)

// The fixture values. They are made up (never a real export) and marked, so the leak check — the
// one the gate runs over the tests' output — can grep for them: a value that shows up in a log, an
// error, an audit text or a test's output is a leak.
const (
	fixturePassword = "fixture-value-Xk7-plugh-Quixote-9b"
	fixtureUsername = "fixture-user-zz31@example.com"
	fixtureAPIKey   = "fixture-value-API-zephyr-5c2d-grault"
	fixtureNotes    = "fixture-notes-caboosetumbler"
	fixtureOTPSeed  = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	fixtureAttach   = "fixture-attachment-tumbler-quartz"
)

// fixtureValues are the strings the leak check greps the tests' output for.
var fixtureValues = []string{fixturePassword, fixtureAPIKey, fixtureNotes, fixtureAttach, fixtureUsername, fixtureOTPSeed}

var (
	owner  = Actor{Name: "owner", Via: "test", Role: "owner"}
	viewer = Actor{Name: "reader", Via: "test", Role: "viewer"}
	oncall = Actor{Name: "nurse", Via: "test", Role: "oncall"}
	agent  = Actor{Name: "somebody / worker", Via: "test", Role: ""} // not a person
)

// newVault opens a store and a vault on it, in a temporary directory, with the seal mode given.
func newVault(t *testing.T, mode string, d Deps) (*Service, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	if d.Log == nil {
		d.Log = testLog(t)
	}
	if d.Now == nil {
		now := time.Now()
		d.Now = func() time.Time { return now }
	}
	st, err := store.Open(filepath.Join(dir, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	d.Store = st
	if d.CheckPassword == nil {
		d.CheckPassword = func(username, password string) error { return nil } // tests do not hash
	}
	s, err := Open(context.Background(), SealConfig{Mode: mode, Dir: filepath.Join(dir, "secrets")}, d)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

func testLog(t *testing.T) *slog.Logger {
	return slog.New(&safeLog{t: t})
}

// safeLog is a slog handler that writes to the test log — so a value that leaks into a log line
// shows up in the output the leak check greps.
type safeLog struct {
	t     *testing.T
	attrs []slog.Attr
}

func (l *safeLog) Enabled(_ context.Context, level slog.Level) bool { return true }
func (l *safeLog) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Level.String())
	b.WriteByte(' ')
	b.WriteString(r.Message)
	write := func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(a.Value.String())
		return true
	}
	for _, a := range l.attrs {
		write(a)
	}
	r.Attrs(write)
	l.t.Log(b.String())
	return nil
}
func (l *safeLog) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &safeLog{t: l.t, attrs: append(append([]slog.Attr(nil), l.attrs...), attrs...)}
}
func (l *safeLog) WithGroup(string) slog.Handler { return l }

// newCollection makes a collection and returns its name.
func newCollection(t *testing.T, s *Service, name string) Collection {
	t.Helper()
	col, err := s.CreateCollection(owner, name, "", LevelReview)
	if err != nil {
		t.Fatal(err)
	}
	return col
}

// putItem makes or changes an item with one field.
func putItem(t *testing.T, s *Service, col, id, field, value string) Item {
	t.Helper()
	in := ItemIn{Collection: col, Title: "Example " + field, Fields: []FieldIn{{Name: field, Type: FieldConcealed, Value: value}}}
	if id != "" {
		in.ID = id
		in.Title = ""
	}
	item, err := s.SetItem(owner, in)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// ---- the key hierarchy and its bindings

// TestItemAADIsBound is the acceptance test for the additional data: a version's ciphertext must
// not open as another item, another version, another collection or another key version. Swapping
// rows in the database gives nothing but authentication failures.
func TestItemAADIsBound(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	other := newCollection(t, s, "Other")
	a := putItem(t, s, col.Name, "", "password", fixturePassword)
	// a second item in the other collection
	b, err := s.SetItem(owner, ItemIn{Collection: other.Name, Title: "Example password", Fields: []FieldIn{{Name: "password", Type: FieldConcealed, Value: fixtureAPIKey}}})
	if err != nil {
		t.Fatal(err)
	}
	var va, vb ItemVersion
	_ = st.Storage().View(func(tx kv.Tx) error {
		va, _ = getVersionTx(tx, a.ID, 1)
		vb, _ = getVersionTx(tx, b.ID, 1)
		return nil
	})
	ck, err := s.collectionKey(col.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(ck)
	if _, err := unwrap(ck, va.CT, itemAAD(a.ID, 1, col.ID, 1)); err != nil {
		t.Fatalf("the right binding must open: %v", err)
	}
	cases := []struct {
		name string
		ad   []byte
		ct   []byte
		key  []byte
	}{
		{"another item", itemAAD(b.ID, 1, col.ID, 1), va.CT, ck},
		{"another version", itemAAD(a.ID, 2, col.ID, 1), va.CT, ck},
		{"another collection", itemAAD(a.ID, 1, other.ID, 1), va.CT, ck},
		{"another key version", itemAAD(a.ID, 1, col.ID, 2), va.CT, ck},
		{"another item's ciphertext under the right binding", itemAAD(a.ID, 1, col.ID, 1), vb.CT, ck},
	}
	for _, c := range cases {
		if _, err := unwrap(c.key, c.ct, c.ad); err == nil {
			t.Errorf("%s: the ciphertext opened where it must not", c.name)
		}
	}
}

// TestPassphraseModeSealsOnRestart is the acceptance test for the passphrase mode: a fresh start is
// sealed, the key functions refuse (while the store itself carries on working), the right passphrase
// unseals, the wrong one does not, and a restart seals again with everything still readable after
// the next unseal.
func TestPassphraseModeSealsOnRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.db")
	seal := SealConfig{Mode: SealPassphrase, Dir: filepath.Join(dir, "secrets")}
	open := func(d Deps) (*Service, *store.Store) {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		d.Store = st
		d.Log = testLog(t)
		d.CheckPassword = func(string, string) error { return nil }
		s, err := Open(context.Background(), seal, d)
		if err != nil {
			t.Fatal(err)
		}
		return s, st
	}
	s1, st1 := open(Deps{Lang: i18n.EN})
	if !s1.Sealed() {
		t.Fatal("a fresh passphrase-mode vault is sealed")
	}
	// the store works while the vault is sealed: monitoring and the SSH channel's world carries on
	if err := st1.Audit(store.AuditEntry{Actor: "test", Via: "test", Action: "probe_ok"}); err != nil {
		t.Fatalf("the audit log must carry on while the vault is sealed: %v", err)
	}
	if err := st1.PutGroup(store.Group{ID: "g1", Key: "k"}); err != nil {
		t.Fatalf("the store must carry on while the vault is sealed: %v", err)
	}
	if _, _, err := s1.Reveal(owner, "anything", nil, ""); !errors.Is(err, ErrSealed) {
		t.Fatalf("a sealed vault refuses to reveal, got %v", err)
	}
	if err := s1.SetPermission(owner, "nope", "user:x", PermRead, ""); !errors.Is(err, ErrSealed) {
		t.Fatalf("a sealed vault refuses policy changes, got %v", err)
	}
	// a fresh passphrase-mode vault takes the first passphrase it is given as its own (it holds
	// nothing yet; the log said to set one before putting anything in)
	if err := s1.InitPassphrase("correct-horse-battery-staple-fixture"); err != nil {
		t.Fatal(err)
	}
	col := newCollection(t, s1, "Infra")
	item := putItem(t, s1, col.Name, "", "password", fixturePassword)
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st1.Close(); err != nil {
		t.Fatal(err)
	}

	// the restart: sealed again, the same passphrase opens it, and the item is still there
	s2, st2 := open(Deps{Lang: i18n.EN})
	defer func() { _ = st2.Close() }()
	if !s2.Sealed() {
		t.Fatal("a restart seals the passphrase-mode vault")
	}
	if err := st2.Audit(store.AuditEntry{Actor: "test", Via: "test", Action: "probe_ok"}); err != nil {
		t.Fatalf("monitoring still works while sealed after a restart: %v", err)
	}
	// now the key check exists, and a wrong passphrase is refused
	if err := s2.Unseal("wrong-passphrase-here"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("a wrong passphrase is refused, got %v", err)
	}
	if !s2.Sealed() {
		t.Fatal("a wrong passphrase leaves the vault sealed")
	}
	if err := s2.Unseal("correct-horse-battery-staple-fixture"); err != nil {
		t.Fatal(err)
	}
	values, _, err := s2.Reveal(owner, item.ID, nil, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if values["password"] != fixturePassword {
		t.Fatal("the value survived the restart and the unseal")
	}
}

// ---- permissions

func TestPermissions(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)

	// nobody but the owner (and those granted) can do anything
	if s.May(viewer, col.ID, PermDescribe) {
		t.Error("a stranger is not let in")
	}
	if err := s.SetPermission(owner, col.Name, "user:reader", PermRead, "the reader reads"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPermission(owner, col.Name, "role:oncall", PermEdit, "the on-call edits"); err != nil {
		t.Fatal(err)
	}
	reader := viewer // Role: viewer, and the user row
	if !s.May(reader, col.ID, PermRead) {
		t.Error("the user row grants read")
	}
	if s.May(reader, col.ID, PermEdit) {
		t.Error("read is not edit")
	}
	if !s.May(oncall, col.ID, PermEdit) {
		t.Error("the role row grants edit")
	}
	if s.May(oncall, col.ID, PermManage) {
		t.Error("edit is not manage")
	}
	if !s.May(owner, col.ID, PermManage) {
		t.Error("the owner manages everywhere")
	}
	// an agent holds nothing at all, whatever rows say
	if s.May(agent, col.ID, PermDescribe) {
		t.Error("an agent is never let in through a person's row")
	}
	// reading needs the password re-entered; with it, values come
	if _, _, err := s.Reveal(reader, item.ID, nil, ""); !errors.Is(err, ErrReauth) {
		t.Fatalf("reveal without the password is refused, got %v", err)
	}
	if _, _, err := s.Reveal(reader, item.ID, nil, "pw"); err != nil {
		t.Fatal(err)
	}
	// within the window, no password again
	if _, _, err := s.Reveal(reader, item.ID, nil, ""); err != nil {
		t.Fatalf("within the window no password is needed, got %v", err)
	}
	// after the window, it is needed again
	s.reauthMu.Lock()
	s.reauth[reauthKey(reader)] = s.now().Add(-time.Hour)
	s.reauthMu.Unlock()
	if _, _, err := s.Reveal(reader, item.ID, nil, ""); !errors.Is(err, ErrReauth) {
		t.Fatalf("after the window the password is needed again, got %v", err)
	}
	// writing needs edit
	if _, err := s.SetItem(reader, ItemIn{ID: item.ID, Collection: col.Name, Fields: []FieldIn{{Name: "password", Value: "nope"}}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read is not write, got %v", err)
	}
	// tags are policy: edit cannot change them, manage can
	editor := Actor{Name: "nurse", Via: "test", Role: "oncall"}
	if _, err := s.SetItem(editor, ItemIn{ID: item.ID, Collection: col.Name, Tags: []string{"env/prod"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("changing tags with edit only is refused, got %v", err)
	}
	if _, err := s.SetItem(editor, ItemIn{ID: item.ID, Collection: col.Name, Actions: []string{"describe"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("changing actions with edit only is refused, got %v", err)
	}
	if _, err := s.SetItem(editor, ItemIn{ID: item.ID, Collection: col.Name, Machines: []string{"edge-1"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("changing machines with edit only is refused, got %v", err)
	}
	if _, err := s.SetItem(editor, ItemIn{ID: item.ID, Collection: col.Name, Hosts: []string{"example.com"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("changing hosts with edit only is refused, got %v", err)
	}
	if _, err := s.SetItem(editor, ItemIn{Collection: col.Name, Title: "Policy new", Level: LevelNever,
		Fields: []FieldIn{{Name: "password", Value: "x"}}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("creating with a level with edit only is refused, got %v", err)
	}
	if _, err := s.SetItem(editor, ItemIn{Collection: col.Name, Title: "Tagged new", Tags: []string{"env/prod"},
		Fields: []FieldIn{{Name: "password", Value: "x"}}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("creating with tags with edit only is refused, got %v", err)
	}
	if _, err := s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name, Tags: []string{"env/prod"}}); err != nil {
		t.Fatal(err)
	}
	// who can get it lists the subjects with read or more
	who := s.WhoCanGet(col.Name)
	if len(who) != 2 { // user:reader and role:oncall
		t.Fatalf("who can get it lists the reader and the on-call, got %v", who)
	}
}

// TestPermissionTamperIsBlocked is the acceptance test for the third invariant: someone holding the
// database file alone widens a permission by writing the row directly — the row's signature does
// not verify, the permission is served as absent, and the tampering is reported to the log and the
// audit log.
func TestPermissionTamperIsBlocked(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	putItem(t, s, col.Name, "", "password", fixturePassword)
	if err := s.SetPermission(owner, col.Name, "user:reader", PermRead, "the reader reads"); err != nil {
		t.Fatal(err)
	}
	reader := viewer
	if !s.May(reader, col.ID, PermRead) {
		t.Fatal("the grant works before the tampering")
	}
	// the tampering: the row is read, widened to manage, and written back directly
	var row []byte
	_ = st.Storage().Update(func(tx kv.Tx) error {
		row = tx.Get(bucketPerms, permRowKey(col.ID, "user:reader"))
		if row == nil {
			t.Fatal("the row is there")
		}
		var p Permission
		if err := json.Unmarshal(row, &p); err != nil {
			t.Fatal(err)
		}
		p.Perm = PermManage
		b, _ := json.Marshal(p)
		return tx.Put(bucketPerms, permRowKey(col.ID, "user:reader"), b)
	})
	// the widened permission is treated as absent: the reader cannot manage, and not even read
	if s.May(reader, col.ID, PermManage) {
		t.Error("the tampered row did not grant manage — it must be treated as absent")
	}
	if s.May(reader, col.ID, PermRead) {
		t.Error("the tampered row is absent, so even the read it carried before is gone")
	}
	// the check runs the report (to the log, the audit log and Bad)
	if bad := s.CheckRows(); len(bad) == 0 {
		t.Error("the tampered row is listed for the console")
	}
	// and the tampering was reported: to the audit log
	found := false
	entries, _, err := st.AuditQuery(store.AuditFilter{Action: "secrets_signature_invalid", Limit: 10})
	if err == nil {
		for _, e := range entries {
			if strings.Contains(e.Detail, "vault row") {
				found = true
			}
		}
	}
	if !found {
		t.Error("the tampering was reported to the audit log")
	}
	// a row made up out of thin air (a signature that was never made) is absent too
	_ = st.Storage().Update(func(tx kv.Tx) error {
		p := Permission{Collection: col.ID, Subject: "user:ghost", Perm: PermManage, At: s.now()}
		b, _ := json.Marshal(p)
		return tx.Put(bucketPerms, permRowKey(col.ID, "user:ghost"), b)
	})
	ghost := Actor{Name: "ghost", Via: "test", Role: "viewer"}
	if s.May(ghost, col.ID, PermDescribe) {
		t.Error("an invented row grants nothing")
	}
}

// ---- agents

// TestAgentsGetNothing holds the fourth invariant at this layer: no agent is given a value, not
// even one whose level is auto, and not even with the vault open and a collection it could read
// about. (Agents come to values through grants, which arrive with the next package.)
func TestAgentsGetNothing(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	if _, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Never given", Level: LevelNever,
		Fields: []FieldIn{{Name: "password", Value: fixturePassword}}}); err != nil {
		t.Fatal(err)
	}
	auto := putItem(t, s, col.Name, "", "password", fixtureAPIKey)
	for _, item := range []string{"Never given", auto.Title} {
		if _, _, err := s.Reveal(agent, item, nil, "pw"); !errors.Is(err, ErrAgent) {
			t.Fatalf("an agent is refused for %q, got %v", item, err)
		}
	}
	// and it sees nothing in the search either
	if out := s.Search(agent, "", "", nil); len(out) != 0 {
		t.Errorf("the search answers an agent with nothing, got %d results", len(out))
	}
}

// ---- record before give

// TestNoRecordNoValue is the first invariant as far as this package reaches it: a view whose
// record cannot be written refuses the values.
func TestNoRecordNoValue(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	d := Deps{Store: st, Lang: i18n.EN, Log: testLog(t), CheckPassword: func(string, string) error { return nil }}
	var refuse bool
	d.Audit = func(e store.AuditEntry) error {
		if refuse && e.Action == "secrets_item_view" {
			return errors.New("the audit log is not taking writes")
		}
		return st.Audit(e)
	}
	s, err := Open(context.Background(), SealConfig{Mode: SealFile, Dir: filepath.Join(dir, "secrets")}, d)
	if err != nil {
		t.Fatal(err)
	}
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	refuse = true
	if _, _, err := s.Reveal(owner, item.ID, nil, "pw"); err == nil {
		t.Fatal("a view whose record cannot be written must refuse the values")
	} else if !strings.Contains(err.Error(), "could not be recorded") {
		t.Fatalf("the refusal says what it is, got %v", err)
	}
}

// ---- the lifecycle

func TestItemLifecycle(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")

	// make
	in := ItemIn{Collection: col.Name, Title: "Example API", Category: "API Credential",
		URLs: []string{"https://api.example.com"},
		Tags: []string{"env/prod", "service/dns"},
		Fields: []FieldIn{
			{Name: "username", Type: FieldString, Purpose: PurposeUsername, Value: fixtureUsername},
			{Name: "credential", Type: FieldConcealed, Purpose: PurposePassword, Value: fixtureAPIKey},
			{Section: "Keys", Name: "otp", Type: FieldOTP, Value: fixtureOTPSeed},
		},
		Notes: fixtureNotes, HasNotes: true, Source: "1password:3f9c2a"}
	item, err := s.SetItem(owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if item.Version != 1 || item.State != StateNormal {
		t.Fatalf("a new item is version 1 and normal, got %d %s", item.Version, item.State)
	}
	// reveal: every value, the otp as a code (six digits), the notes
	values, fields, err := s.Reveal(owner, item.ID, nil, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 {
		t.Fatalf("three fields, got %d", len(fields))
	}
	if values["username"] != fixtureUsername || values["credential"] != fixtureAPIKey {
		t.Error("the values round-trip")
	}
	if values["Keys\x00otp"] == fixtureOTPSeed {
		t.Error("an otp field is never given as its seed")
	}
	code := values["Keys\x00otp"]
	if len(code) != 6 {
		t.Errorf("the otp answers with a six-digit code, got %q", code)
	}
	if values["notes"] != fixtureNotes {
		t.Error("the notes round-trip")
	}
	// change one field: the others keep their values, the version moves
	if _, err := s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name,
		Fields: []FieldIn{{Name: "credential", Value: "rotated-fixture-value-2"}}}); err != nil {
		t.Fatal(err)
	}
	values, _, err = s.Reveal(owner, item.ID, []string{"credential", "username"}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if values["credential"] != "rotated-fixture-value-2" {
		t.Error("the changed field is the new one")
	}
	values, _, _ = s.Reveal(owner, item.ID, nil, "pw")
	if values["username"] != fixtureUsername {
		t.Error("an unnamed field keeps its value")
	}
	rollback := func() {
		t.Helper()
		if _, err := s.RollbackItem(owner, item.ID, 1, "back to the first"); err != nil {
			t.Fatal(err)
		}
	}
	rollback()
	values, _, _ = s.Reveal(owner, item.ID, nil, "pw")
	if values["credential"] != fixtureAPIKey {
		t.Error("the rollback restored the first version's value")
	}
	if v := s.Versions(item.ID); len(v) != 3 {
		t.Errorf("three versions after the change and the rollback, got %d", len(v))
	}
	// generate: a new value the caller never sees; the reveal finds it
	before, err := GenerateValue(GenSpec{Length: 20, Charset: "alnum"})
	if err != nil {
		t.Fatal(err)
	}
	_ = before
	if item, err = s.Generate(owner, item.ID, "", "credential", "quarterly turn", GenSpec{Length: 20, Charset: "alnum"}); err != nil {
		t.Fatal(err)
	}
	values, _, _ = s.Reveal(owner, item.ID, []string{"credential"}, "pw")
	if values["credential"] == fixtureAPIKey || len(values["credential"]) != 20 {
		t.Errorf("the generated value replaced the field, got %q", values["credential"])
	}

	// archive, trash, restore
	if _, err := s.ArchiveItem(owner, item.ID); err != nil {
		t.Fatal(err)
	}
	if it, _, _ := s.Item(item.ID); it.State != StateArchived {
		t.Error("archived")
	}
	if out := s.Search(owner, "Example API", "", nil); len(out) != 0 {
		t.Error("the archive stays out of the search")
	}
	if _, err := s.TrashItem(owner, item.ID); err != nil {
		t.Fatal(err)
	}
	if it, _, _ := s.Item(item.ID); it.State != StateTrash {
		t.Error("trashed")
	}
	if _, err := s.RestoreItem(owner, item.ID); err != nil {
		t.Fatal(err)
	}
	if it, _, _ := s.Item(item.ID); it.State != StateNormal {
		t.Error("restored")
	}
	// attachments
	item, err = s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name,
		Attachments: []AttachmentIn{{Name: "note.txt", Data: []byte(fixtureAttach)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Reveal(owner, item.ID, nil, "pw"); err != nil {
		t.Fatal(err)
	}
	data, _, err := s.Attachment(owner, item.ID, item.Version, "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != fixtureAttach {
		t.Error("the attachment round-trips")
	}
	// too large is refused
	big := make([]byte, 11<<20)
	if _, err := s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name,
		Attachments: []AttachmentIn{{Name: "big.bin", Data: big}}}); !errors.Is(err, ErrAttachment) {
		t.Fatalf("an attachment over the limit is refused, got %v", err)
	}
}

// TestTrashPurge checks the retention: a trashed item goes when its time is up.
func TestTrashPurge(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	if _, err := s.TrashItem(owner, item.ID); err != nil {
		t.Fatal(err)
	}
	if n := s.PurgeTrash(); n != 0 {
		t.Fatalf("nothing goes before the retention, got %d", n)
	}
	// push the trashed-at back beyond the retention (thirty days by default)
	var trashed signedhead.Head
	_ = st.Storage().Update(func(tx kv.Tx) error {
		var r signedItem
		v := tx.Get(bucketItems, []byte(item.ID))
		if json.Unmarshal(v, &r) != nil {
			t.Fatal("the row is there")
		}
		r.TrashedAt = s.now().Add(-40 * 24 * time.Hour)
		h, err := s.putItemTx(tx, r.Item)
		if err != nil {
			return err
		}
		trashed = h
		return nil
	})
	s.afterHeads(trashed)
	if n := s.PurgeTrash(); n != 1 {
		t.Fatalf("the item goes when its time is up, got %d", n)
	}
	if _, _, ok := s.Item(item.ID); ok {
		t.Error("the item is gone")
	}
}

// ---- levels and tags

func TestEffectiveLevel(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra") // review
	var err error
	if _, err := s.SetTag(owner, "env/prod", LevelPerson, "production is personal"); err != nil {
		t.Fatal(err)
	}
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	if got := s.EffectiveLevel(item); got != LevelReview {
		t.Fatalf("an untagged item takes the collection's default, got %s", got)
	}
	if item, err = s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name, Tags: []string{"env/prod"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveLevel(item); got != LevelPerson {
		t.Fatalf("the tag's level is taken, got %s", got)
	}
	// the item's own override beats the tag
	if item, err = s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name, Level: LevelAuto}); err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveLevel(item); got != LevelAuto {
		t.Fatalf("the item's own override wins, got %s", got)
	}
	// the strictest of several tags
	if _, err := s.SetTag(owner, "kind/personal", LevelNever, ""); err != nil {
		t.Fatal(err)
	}
	if item, err = s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name, Level: "-", Tags: []string{"env/prod", "kind/personal"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("the strictest tag wins, got %s", got)
	}
}

// ---- rotation

func TestRotateRootKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.db")
	openBoth := func() (*Service, *store.Store) {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		s, err := Open(context.Background(), SealConfig{Mode: SealFile, Dir: filepath.Join(dir, "secrets")},
			Deps{Store: st, Lang: i18n.EN, Log: testLog(t), CheckPassword: func(string, string) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return s, st
	}
	s, st := openBoth()
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	oldRK := mustRead(t, filepath.Join(dir, "secrets", fileRootKey))
	if err := s.RotateRootKey(""); err != nil {
		t.Fatal(err)
	}
	if newRK := mustRead(t, filepath.Join(dir, "secrets", fileRootKey)); string(newRK) == string(oldRK) {
		t.Error("the root key file moved")
	}
	values, _, err := s.Reveal(owner, item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("the values survive a root-key rotation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// and the next start opens with the new key
	s2, st2 := openBoth()
	defer func() { _ = st2.Close() }()
	values, _, err = s2.Reveal(owner, item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("the values survive a restart after the rotation: %v", err)
	}
}

func TestRotateCollectionKey(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	if err := s.RotateCollectionKey(col.Name); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); s.rotatingNow(col.ID); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the rotation did not finish")
		}
	}
	if after, _ := s.Collection(col.Name); after.KeyVersion != 2 {
		t.Fatalf("the collection's key moved to version 2, got %d", after.KeyVersion)
	}
	values, _, err := s.Reveal(owner, item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("the values survive a collection-key rotation: %v", err)
	}
	// the old ciphertext is gone: every version row now binds to key version 2
	var wrongKeyVersion int
	_ = st.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketVersions, kv.Range{Prefix: []byte(item.ID + "\x00")}, func(_, v []byte) bool {
			var ver ItemVersion
			if json.Unmarshal(v, &ver) == nil && ver.KeyVersion != 2 {
				wrongKeyVersion++
			}
			return true
		})
		return nil
	})
	if wrongKeyVersion != 0 {
		t.Errorf("%d version rows still bind to the old key version", wrongKeyVersion)
	}
}

// TestRemoveMemberDoesNotRotateKeys holds the design's promise that removing a member changes no
// key: the wrapped collection key rows are untouched.
func TestRemoveMemberDoesNotRotateKeys(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	var before []byte
	_ = st.Storage().View(func(tx kv.Tx) error {
		before = append([]byte(nil), tx.Get(bucketKeys, keyRowKey(col.ID, 1))...)
		return nil
	})
	if err := s.SetPermission(owner, col.Name, "user:reader", PermRead, ""); err != nil {
		t.Fatal(err)
	}
	_ = s.SetPermission(owner, col.Name, "user:reader", "", "") // removed again
	var after []byte
	_ = st.Storage().View(func(tx kv.Tx) error {
		after = tx.Get(bucketKeys, keyRowKey(col.ID, 1))
		return nil
	})
	if string(before) != string(after) {
		t.Error("removing a member must not change the collection key")
	}
}

func reopenVault(t *testing.T, s *Service, st *store.Store) *Service {
	t.Helper()
	dir, mode := s.cfg.Dir, s.cfg.Mode
	_ = s.Close()
	s2, err := Open(context.Background(), SealConfig{Mode: mode, Dir: dir}, Deps{Store: st, Lang: i18n.EN, Log: testLog(t),
		CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return s2
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---- references and otp

func TestRef(t *testing.T) {
	r, err := ParseRef("beacon://Infra/Example API/password")
	if err != nil || r.Collection != "Infra" || r.Item != "Example API" || r.Field != "password" {
		t.Fatalf("a plain reference: %+v %v", r, err)
	}
	r, err = ParseRef("beacon://Infra/Example/Keys/otp?attribute=otp")
	if err != nil || r.Section != "Keys" || r.Field != "otp" || r.Attribute != "otp" {
		t.Fatalf("a sectioned reference with the otp attribute: %+v %v", r, err)
	}
	for _, bad := range []string{"Infra/Example/password", "beacon://onlyone", "beacon://a/b/c/d/e"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("%q must not parse", bad)
		}
	}
	if RefNameOK("weird:name") || RefNameOK("") {
		t.Error("a name with a colon is addressed by ID")
	}
	if !RefNameOK("Example API-2_.d") {
		t.Error("letters, digits, spaces, - _ and . are fine")
	}
	if got := RefOf("Infra", "Example", "Keys", "otp"); got != "beacon://Infra/Example/Keys/otp" {
		t.Errorf("RefOf round-trips, got %s", got)
	}
}

func TestOTP(t *testing.T) {
	// the RFC 6238 test vectors for SHA-1 (six digits), with the standard seed
	seed := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	vectors := []struct {
		unix int64
		code string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, v := range vectors {
		got, err := TOTP(seed, time.Unix(v.unix, 0).UTC(), 6)
		if err != nil {
			t.Fatal(err)
		}
		if got != v.code {
			t.Errorf("at %d: got %s, want %s", v.unix, got, v.code)
		}
	}
	// the seconds it has left
	_, left, err := TOTPNow(seed, time.Unix(59, 0).UTC())
	if err != nil || left <= 0 || left > TOTPPeriod {
		t.Errorf("the code has between 1 and 30 seconds left, got %v (%v)", left, err)
	}
	if _, err := TOTP("not base32!!", time.Now(), 6); err == nil {
		t.Error("a seed that is not base32 is refused")
	}
}

// ---- search

func TestSearch(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	infra := newCollection(t, s, "Infra")
	other := newCollection(t, s, "Other")
	putItem(t, s, infra.Name, "", "password", fixturePassword) // "Example password"
	putItem(t, s, other.Name, "", "credential", fixtureAPIKey) // "Example credential"
	if out := s.Search(owner, "password", "", nil); len(out) != 1 {
		t.Fatalf("the search finds by title, got %d", len(out))
	}
	if out := s.Search(owner, "", other.Name, nil); len(out) != 1 {
		t.Fatalf("the search narrows by collection, got %d", len(out))
	}
	if out := s.Search(viewer, "", "", nil); len(out) != 0 {
		t.Fatalf("a stranger finds nothing, got %d", len(out))
	}
	// a permission to describe opens the overview, not the values
	if err := s.SetPermission(owner, infra.Name, "role:viewer", PermDescribe, ""); err != nil {
		t.Fatal(err)
	}
	if out := s.Search(viewer, "", "", nil); len(out) != 1 {
		t.Fatalf("describe opens the overview, got %d", len(out))
	}
	if _, _, err := s.Reveal(viewer, "Example password", nil, "pw"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("describe does not read values, got %v", err)
	}
}

// ---- the settings' migration onto SK

func TestSettingsMigrateOntoSK(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	secretsDir := filepath.Join(dir, "secrets")
	sset, err := settings.Open(settings.Deps{Store: st, Lang: i18n.EN, Log: testLog(t), EpochsDir: secretsDir})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), SealConfig{Mode: SealFile, Dir: secretsDir},
		Deps{Store: st, Settings: sset, Lang: i18n.EN, Log: testLog(t), CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	sk := s.SK()
	if len(sk) != 32 {
		t.Fatal("the vault gives the settings a 32-byte key")
	}
	if err := sset.SetKey(sk); err != nil {
		t.Fatal(err)
	}
	if _, err := sset.Set(context.Background(), settings.Actor{Name: "owner", Via: "test"}, settings.ScopeSystem, "", "request.reason_min_length", json.RawMessage("12"), "tightening", 0); err != nil {
		t.Fatal(err)
	}
	res, err := sset.System("request.reason_min_length")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Value) != "12" {
		t.Fatalf("the row survived SetKey, got %s", res.Value)
	}
	if len(sset.Bad()) != 0 {
		t.Fatalf("no row is bad after SetKey, got %v", sset.Bad())
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		var rows [][]byte
		tx.Range([]byte("settings"), kv.Range{}, func(k, v []byte) bool {
			if string(k) == "\x00head" {
				return true
			}
			rows = append(rows, append([]byte(nil), k...))
			return true
		})
		if len(rows) == 0 {
			t.Fatal("the row is there")
		}
		var r settings.Row
		if err := json.Unmarshal(tx.Get([]byte("settings"), rows[0]), &r); err != nil {
			return err
		}
		r.Value = json.RawMessage(`1`)
		b, _ := json.Marshal(r)
		return tx.Put([]byte("settings"), rows[0], b)
	})
	if res, _ = sset.System("request.reason_min_length"); string(res.Value) == "1" {
		t.Fatal("a tampered settings row is served as absent")
	}
	sealed, err := settings.Open(settings.Deps{Store: st, Lang: i18n.EN, Log: testLog(t), EpochsDir: secretsDir})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := sealed.System("request.reason_min_length"); err != nil || string(res.Value) == "12" {
		t.Fatalf("without a key the failsafe answers, got %s %v", res.Value, err)
	}
	if _, err := sealed.Set(context.Background(), settings.Actor{Name: "owner", Via: "test"}, settings.ScopeSystem, "", "request.reason_min_length", json.RawMessage("10"), "", 0); err == nil {
		t.Fatal("without a key no setting can change")
	}
}

// ---- sealed vault

func TestSealedVaultRefusesKeyWork(t *testing.T) {
	s, _ := newVault(t, SealPassphrase, Deps{Lang: i18n.EN})
	if err := s.InitPassphrase("fixture-passphrase-hush-puppy"); err != nil {
		t.Fatal(err)
	}
	col := newCollection(t, s, "Infra")
	if err := s.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCollection(owner, "Another", "", LevelAuto); !errors.Is(err, ErrSealed) {
		t.Fatalf("a sealed vault makes no collections, got %v", err)
	}
	if _, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Nope", Fields: []FieldIn{{Name: "password", Value: "x"}}}); !errors.Is(err, ErrSealed) {
		t.Fatalf("a sealed vault writes no items, got %v", err)
	}
	if err := s.RotateRootKey(""); !errors.Is(err, ErrSealed) {
		t.Fatalf("a sealed vault rotates nothing, got %v", err)
	}
	if err := s.Unseal("fixture-passphrase-hush-puppy"); err != nil {
		t.Fatal(err)
	}
	if cols := s.Collections(); len(cols) != 1 {
		t.Fatalf("after the unseal everything is there again, got %d collections", len(cols))
	}
}

// ---- the generate helper

func TestGenerateValue(t *testing.T) {
	if v, err := GenerateValue(GenSpec{}); err != nil || len(v) != 24 {
		t.Fatalf("the default is 24 characters, got %d (%v)", len(v), err)
	}
	v, err := GenerateValue(GenSpec{Length: 40, Charset: "digits?"})
	if err == nil {
		t.Fatalf("an unknown charset is refused, got %q", v)
	}
	a, _ := GenerateValue(GenSpec{Length: 32, Charset: "alnum"})
	b, _ := GenerateValue(GenSpec{Length: 32, Charset: "alnum"})
	if a == b {
		t.Fatal("two generated values differ")
	}
	for _, c := range b {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", c) {
			t.Fatalf("alnum stays alnum, got %q", b)
		}
	}
}

// ---- audit coverage

// TestAuditCoversTheLifecycle checks that making, changing, archiving, removing, restoring and
// viewing all leave their entries — and that no entry holds a value.
func TestAuditCoversTheLifecycle(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	if _, err := s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name, Fields: []FieldIn{{Name: "password", Value: "rotated-fixture-value-3"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveItem(owner, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrashItem(owner, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreItem(owner, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RollbackItem(owner, item.ID, 1, "back"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Generate(owner, item.ID, "", "password", "turn", GenSpec{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Reveal(owner, item.ID, nil, "pw"); err != nil {
		t.Fatal(err)
	}
	want := []string{"secrets_item_create", "secrets_item_update", "secrets_item_archive", "secrets_item_delete",
		"secrets_item_restore", "secrets_item_rollback", "secrets_item_generate", "secrets_item_view",
		"secrets_collection_create"}
	got := map[string]bool{}
	entries, _, err := st.AuditQuery(store.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		got[e.Action] = true
		for _, v := range append([]string{"rotated-fixture-value-3"}, fixtureValues...) {
			if strings.Contains(e.Detail, v) || strings.Contains(e.Target, v) {
				t.Errorf("the audit entry for %s holds a value", e.Action)
			}
		}
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("no audit entry for %s", w)
		}
	}
}

// ---- file mode basics

func TestFileModeFirstAndSecondStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.db")
	cfg := SealConfig{Mode: SealFile, Dir: filepath.Join(dir, "secrets")}
	st1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := Open(context.Background(), cfg, Deps{Store: st1, Lang: i18n.EN, Log: testLog(t), CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if s1.Sealed() {
		t.Fatal("the file mode unseals at the first start")
	}
	col := newCollection(t, s1, "Infra")
	item := putItem(t, s1, col.Name, "", "password", fixturePassword)
	_ = s1.Close()
	_ = st1.Close()

	// a replaced root key file does not open the vault
	goodKey := append([]byte(nil), mustRead(t, filepath.Join(dir, "secrets", fileRootKey))...)
	if err := writeKeyFile(filepath.Join(dir, "secrets", fileRootKey), make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	if _, err := Open(context.Background(), cfg, Deps{Store: st2, Lang: i18n.EN, Log: testLog(t), CheckPassword: func(string, string) error { return nil }}); err == nil {
		t.Fatal("a wrong root key file refuses to open the vault")
	} else if !strings.Contains(err.Error(), "does not open this vault") {
		t.Fatalf("the refusal says what it is, got %v", err)
	}
	// the right key back: the vault opens and the item is there
	if err := writeKeyFile(filepath.Join(dir, "secrets", fileRootKey), goodKey); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(context.Background(), cfg, Deps{Store: st2, Lang: i18n.EN, Log: testLog(t), CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s2.Item(item.ID); !ok {
		t.Fatal("with the right key back, the item is there")
	}
}

func TestCopiedPermissionRowDoesNotGrant(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	sandbox := newCollection(t, s, "Sandbox")
	prod := newCollection(t, s, "Prod")
	if err := s.SetPermission(owner, sandbox.Name, "user:bob", PermManage, "bob manages sandbox"); err != nil {
		t.Fatal(err)
	}
	bob := Actor{Name: "bob", Via: "test", Role: "viewer"}
	if !s.May(bob, sandbox.ID, PermManage) {
		t.Fatal("the grant works on sandbox")
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		row := tx.Get(bucketPerms, permRowKey(sandbox.ID, "user:bob"))
		return tx.Put(bucketPerms, permRowKey(prod.ID, "role:viewer"), row)
	})
	if s.May(viewer, prod.ID, PermManage) {
		t.Fatal("a copied permission row must not grant manage on another collection")
	}
	if s.May(viewer, prod.ID, PermDescribe) {
		t.Fatal("a copied permission row must not grant anything on another collection")
	}
	if _, err := s.CreateCollection(owner, "Other", "", LevelReview); err != nil {
		t.Fatal(err)
	}
	if s.May(viewer, prod.ID, PermManage) {
		t.Fatal("after an unrelated write the copied row still grants nothing")
	}
	s2 := reopenVault(t, s, st)
	if s2.May(viewer, prod.ID, PermManage) {
		t.Fatal("after reopen the copied row still grants nothing")
	}
}

func TestCopiedVersionDoesNotReveal(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	a := putItem(t, s, col.Name, "", "password", fixturePassword)
	b := putItem(t, s, col.Name, "", "password", fixtureAPIKey)
	_ = st.Storage().Update(func(tx kv.Tx) error {
		va, _ := getVersionTx(tx, a.ID, 1)
		raw, _ := json.Marshal(va)
		return tx.Put(bucketVersions, versionRowKey(b.ID, 1), raw)
	})
	if _, _, err := s.Reveal(owner, b.ID, nil, "pw"); err == nil {
		t.Fatal("a version copied onto another item must not open")
	}
}

func TestFieldTypeSwapDoesNotRevealSeed(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "OTP",
		Fields: []FieldIn{{Name: "otp", Type: FieldOTP, Value: fixtureOTPSeed}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		ver, _ := getVersionTx(tx, item.ID, 1)
		for i := range ver.Fields {
			ver.Fields[i].Type = FieldConcealed
		}
		b, _ := json.Marshal(ver)
		return tx.Put(bucketVersions, versionRowKey(item.ID, 1), b)
	})
	values, _, err := s.Reveal(owner, item.ID, nil, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if values["otp"] == fixtureOTPSeed {
		t.Fatal("swapping the field type in the clear must not give the seed")
	}
}

func TestUnknownCollectionAndTagAreNever(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	item.Collection = "gone"
	item.Level = ""
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("a missing collection is never, got %s", got)
	}
	item.Collection = col.ID
	item.Tags = []string{"no/such"}
	if got := s.EffectiveLevel(item); got != col.Level {
		t.Fatalf("a tag that was never a policy tag is a label, got %s", got)
	}
}

func TestUnsealDoesNotTakeTheFirstPassphrase(t *testing.T) {
	s, _ := newVault(t, SealPassphrase, Deps{Lang: i18n.EN})
	if err := s.Unseal("first-passphrase-here"); !errors.Is(err, ErrNotInitialised) {
		t.Fatalf("Unseal before init: %v", err)
	}
	if !s.Sealed() {
		t.Fatal("a refused Unseal must leave the vault sealed")
	}
	if err := s.InitPassphrase("first-passphrase-here"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unseal("other-passphrase-here"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("a later passphrase is not taken as a new init: %v", err)
	}
}

func TestEditDoesNotTakePlaintextFieldType(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "OTP",
		Fields: []FieldIn{{Name: "otp", Type: FieldOTP, Value: fixtureOTPSeed}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name,
		Fields: []FieldIn{{Name: "otp", Type: FieldConcealed, Value: fixtureOTPSeed}}}); err != nil {
		t.Fatal(err)
	}
	values, fields, err := s.Reveal(owner, item.ID, nil, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if values["otp"] == fixtureOTPSeed {
		t.Fatal("an edit must not take a swapped plaintext type and give the seed")
	}
	for _, f := range fields {
		if f.Name == "otp" && f.Type != FieldOTP {
			t.Fatalf("the ciphertext type stays otp, got %s", f.Type)
		}
	}
}

func TestRevealEmptyCiphertextFieldsDoNotFallBack(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "OTP",
		Fields: []FieldIn{{Name: "otp", Type: FieldOTP, Value: fixtureOTPSeed}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		ver, ok := getVersionTx(tx, item.ID, 1)
		if !ok {
			return errors.New("the version is there")
		}
		d, err := s.decryptDetailsTx(tx, col, ver, item.ID, 1)
		if err != nil {
			return err
		}
		d.Fields = nil
		ck, err := s.collectionKeyByVersionTx(tx, col.ID, col.KeyVersion)
		if err != nil {
			return err
		}
		defer clear(ck)
		plain, err := json.Marshal(d)
		if err != nil {
			return err
		}
		ver.CT, err = wrap(ck, plain, itemAAD(item.ID, 1, col.ID, col.KeyVersion))
		if err != nil {
			return err
		}
		b, err := json.Marshal(ver)
		if err != nil {
			return err
		}
		return tx.Put(bucketVersions, versionRowKey(item.ID, 1), b)
	})
	values, _, err := s.Reveal(owner, item.ID, nil, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if values["otp"] == fixtureOTPSeed {
		t.Fatal("empty ciphertext fields must not fall back to the plaintext field list")
	}
	if values["otp"] != "" {
		t.Fatal("empty ciphertext fields must not mint a code from the plaintext list")
	}
}

func TestRevealWrongPasswordIsRateLimited(t *testing.T) {
	now := time.Now()
	s, _ := newVault(t, SealFile, Deps{
		Lang:          i18n.EN,
		CheckPassword: func(string, string) error { return errors.New("no") },
		Now:           func() time.Time { return now },
	})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	for i := 0; i < maxRevealFails-1; i++ {
		if _, _, err := s.Reveal(owner, item.ID, nil, "wrong"); !errors.Is(err, ErrReauth) {
			t.Fatalf("fail %d: %v", i+1, err)
		}
	}
	if _, _, err := s.Reveal(owner, item.ID, nil, "wrong"); !errors.Is(err, ErrRevealLocked) {
		t.Fatalf("the tenth wrong password locks, got %v", err)
	}
	if _, _, err := s.Reveal(owner, item.ID, nil, "wrong"); !errors.Is(err, ErrRevealLocked) {
		t.Fatalf("still locked, got %v", err)
	}
	if d := RevealLockedWait(&revealLockedError{wait: time.Minute}); d != time.Minute {
		t.Fatalf("typed wait: %s", d)
	}
	now = now.Add(revealFailWindow)
	if _, _, err := s.Reveal(owner, item.ID, nil, "wrong"); !errors.Is(err, ErrReauth) {
		t.Fatalf("after the window a wrong password is reauth again, got %v", err)
	}
}

func TestUnprefixedSignatureIsRefused(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	sk, err := s.sk()
	if err != nil {
		t.Fatal(err)
	}
	var committed signedhead.Head
	if err := st.Storage().Update(func(tx kv.Tx) error {
		raw := tx.Get(bucketCollections, []byte(col.ID))
		var r signedCollection
		if json.Unmarshal(raw, &r) != nil {
			t.Fatal("the collection row")
		}
		r.Sig = signRow(sk, r.ID, r.Name, r.Description, r.Level, i64(r.KeyVersion), r.CreatedBy, i64(r.Created.UnixNano()))
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := tx.Put(bucketCollections, []byte(col.ID), b); err != nil {
			return err
		}
		h, err := s.commitPolicy(tx, map[string][]byte{policyRowKey(kindCollection, col.ID): b}, nil)
		committed = h
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s.afterHeads(committed)
	if _, ok := s.Collection(col.Name); ok {
		t.Fatal("a row signed without the kind prefix must not verify")
	}
}

func TestCopiedTagRowDoesNotApply(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	if _, err := s.SetTag(owner, "env/prod", LevelNever, ""); err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		row := tx.Get(bucketTags, []byte("env/prod"))
		return tx.Put(bucketTags, []byte("env/dev"), row)
	})
	if _, ok := s.tagByName("env/dev"); ok {
		t.Fatal("a tag copied under another name must not apply")
	}
}

type testBot struct{ texts []string }

func (b *testBot) Send(_ context.Context, t string) error {
	b.texts = append(b.texts, t)
	return nil
}

func (b *testBot) heard(part string) bool {
	for _, t := range b.texts {
		if strings.Contains(t, part) {
			return true
		}
	}
	return false
}

// TestReplayOldTagRowStaysStrict is the anti-replay of a looser tag: the head still names never,
// so putting the earlier auto row back does not loosen the item.
func TestReplayOldTagRowStaysStrict(t *testing.T) {
	bot := &testBot{}
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN, Bot: bot})
	col := newCollection(t, s, "Infra")
	if _, err := s.SetTag(owner, "env/prod", LevelAuto, ""); err != nil {
		t.Fatal(err)
	}
	var loose []byte
	_ = st.Storage().View(func(tx kv.Tx) error {
		loose = append([]byte(nil), tx.Get(bucketTags, []byte("env/prod"))...)
		return nil
	})
	if _, err := s.SetTag(owner, "env/prod", LevelNever, ""); err != nil {
		t.Fatal(err)
	}
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Tagged", Tags: []string{"env/prod"},
		Fields: []FieldIn{{Name: "password", Value: fixturePassword}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("after the stricter tag, got %s", got)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketTags, []byte("env/prod"), loose)
	})
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("replaying the looser tag row must stay never, got %s", got)
	}
	if !bot.heard("strictest") {
		t.Error("the stale tag was reported")
	}
	if _, err := s.CreateCollection(owner, "Other", "", LevelReview); err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("after an unrelated write the looser tag still stays never, got %s", got)
	}
	s2 := reopenVault(t, s, st)
	item2, _, ok := s2.Item(item.ID)
	if !ok {
		t.Fatal("the item is still there")
	}
	if got := s2.EffectiveLevel(item2); got != LevelNever {
		t.Fatalf("after reopen the looser tag still stays never, got %s", got)
	}
}

// TestDeletedTagRowStaysStrict is deleting a stricter tag row from the file without DeleteTag:
// the head still names it, so the item stays never.
func TestDeletedTagRowStaysStrict(t *testing.T) {
	bot := &testBot{}
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN, Bot: bot})
	col := newCollection(t, s, "Infra")
	if _, err := s.SetTag(owner, "env/prod", LevelNever, ""); err != nil {
		t.Fatal(err)
	}
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Tagged", Tags: []string{"env/prod"},
		Fields: []FieldIn{{Name: "password", Value: fixturePassword}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		return tx.Delete(bucketTags, []byte("env/prod"))
	})
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("a deleted stricter tag row must stay never, got %s", got)
	}
	if !bot.heard("strictest") {
		t.Error("the missing tag was reported")
	}
	if _, err := s.CreateCollection(owner, "Other", "", LevelReview); err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("after an unrelated write a deleted stricter tag still stays never, got %s", got)
	}
	s2 := reopenVault(t, s, st)
	item2, _, ok := s2.Item(item.ID)
	if !ok {
		t.Fatal("the item is still there")
	}
	if got := s2.EffectiveLevel(item2); got != LevelNever {
		t.Fatalf("after reopen a deleted stricter tag still stays never, got %s", got)
	}
	s = s2
	// DeleteTag records the drop in the head, so the name becomes a label
	if _, err := s.SetTag(owner, "env/prod", LevelNever, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTag(owner, "env/prod"); err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveLevel(item); got != col.Level {
		t.Fatalf("after DeleteTag the name is a label, got %s", got)
	}
}

func TestAttachmentNeedsReauthAndRecord(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "beacon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var refuse bool
	d := Deps{Store: st, Lang: i18n.EN, Log: testLog(t), CheckPassword: func(string, string) error { return nil }}
	d.Audit = func(e store.AuditEntry) error {
		if refuse && e.Action == "secrets_item_view" {
			return errors.New("the audit log is not taking writes")
		}
		return st.Audit(e)
	}
	s, err := Open(context.Background(), SealConfig{Mode: SealFile, Dir: filepath.Join(dir, "secrets")}, d)
	if err != nil {
		t.Fatal(err)
	}
	col := newCollection(t, s, "Infra")
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "File",
		Fields:      []FieldIn{{Name: "password", Value: fixturePassword}},
		Attachments: []AttachmentIn{{Name: "note.txt", Data: []byte("ok")}}})
	if err != nil {
		t.Fatal(err)
	}
	s.reauthMu.Lock()
	s.reauth = map[string]time.Time{}
	s.reauthMu.Unlock()
	if _, _, err := s.Attachment(owner, item.ID, 1, "note.txt"); !errors.Is(err, ErrReauth) {
		t.Fatalf("download without reauth is refused, got %v", err)
	}
	if _, _, err := s.Reveal(owner, item.ID, nil, "pw"); err != nil {
		t.Fatal(err)
	}
	refuse = true
	data, _, err := s.Attachment(owner, item.ID, 1, "note.txt")
	if err == nil {
		t.Fatal("a download whose record cannot be written must refuse the bytes")
	}
	if len(data) != 0 {
		t.Fatal("no bytes are returned when the record cannot be written")
	}
}

func TestRotateRootKeyWriteFailureKeepsTheVault(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	s.writeKeyFileFn = func(string, []byte) error { return errors.New("the disk is full") }
	if err := s.RotateRootKey(""); err == nil {
		t.Fatal("a write failure must refuse the rotation")
	}
	values, _, err := s.Reveal(owner, item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("the vault still opens with the original key: %v", err)
	}
}

func TestRotateRootKeyCrashAfterCommitOpensWithStagedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.db")
	secretsDir := filepath.Join(dir, "secrets")
	seal := SealConfig{Mode: SealFile, Dir: secretsDir}
	open := func() (*Service, *store.Store, *settings.Service) {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		sset, err := settings.Open(settings.Deps{Store: st, Lang: i18n.EN, Log: testLog(t), EpochsDir: secretsDir})
		if err != nil {
			t.Fatal(err)
		}
		s, err := Open(context.Background(), seal, Deps{Store: st, Settings: sset, Lang: i18n.EN, Log: testLog(t),
			CheckPassword: func(string, string) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		if !s.Sealed() {
			if err := sset.SetKey(s.SK()); err != nil {
				t.Fatal(err)
			}
		}
		return s, st, sset
	}
	s, st, sset := open()
	if _, err := sset.Set(context.Background(), settings.Actor{Name: "owner", Via: "test"}, settings.ScopeSystem, "", "request.max_uses", json.RawMessage("5"), "tighten the uses", 0); err != nil {
		t.Fatal(err)
	}
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	s.replaceFileFn = func(string, string) error { return nil } // the rename never happens
	if err := s.RotateRootKey(""); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	_ = st.Close()
	s2, st2, sset2 := open()
	defer func() { _ = st2.Close() }()
	values, _, err := s2.Reveal(owner, item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("a crash between commit and replace must still open the vault: %v", err)
	}
	res, err := sset2.System("request.max_uses")
	if err != nil || string(res.Value) != "5" {
		t.Fatalf("settings must survive the crash after commit, got %s %v", res.Value, err)
	}
}

func TestRotateRootKeyKMSWrapFailureKeepsTheVault(t *testing.T) {
	dir := t.TempDir()
	cfg := SealConfig{Mode: SealTransit, Dir: filepath.Join(dir, "secrets")}
	server, _ := fakeKeepers(t, &cfg, dir)
	defer server.Close()
	st := newTestStore(t, filepath.Join(dir, "beacon.db"))
	s, err := Open(context.Background(), cfg, Deps{Store: st, Lang: i18n.EN, Log: testLog(t),
		CheckPassword: func(string, string) error { return nil }, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	s.keeperFn = func() (kmsClient, error) {
		return failEncrypt{}, nil
	}
	if err := s.RotateRootKey(""); err == nil {
		t.Fatal("a KMS wrap failure must refuse the rotation")
	}
	values, _, err := s.Reveal(owner, item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("the vault still opens with the original key: %v", err)
	}
}

type failEncrypt struct{}

func (failEncrypt) encrypt(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("the key keeper refused")
}
func (failEncrypt) decrypt(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("the key keeper refused")
}

func TestEditCannotSetPolicyOnCreate(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	if err := s.SetPermission(owner, col.Name, "user:nurse", PermEdit, "edits"); err != nil {
		t.Fatal(err)
	}
	editor := Actor{Name: "nurse", Via: "test", Role: "oncall"}
	if _, err := s.SetItem(editor, ItemIn{Collection: col.Name, Title: "Plain",
		Fields: []FieldIn{{Name: "password", Value: "x"}}}); err != nil {
		t.Fatalf("edit may create an item without policy fields: %v", err)
	}
}

func TestNULInSignedFieldsIsRefused(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	if _, err := s.CreateCollection(owner, "Infra", "desc\x00oops", LevelReview); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a NUL in a signed field is refused, got %v", err)
	}
}

func TestReauthWindowIsPerVia(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	console := Actor{Name: "owner", Via: "console", Role: "owner"}
	api := Actor{Name: "owner", Via: "api", Role: "owner"}
	if _, _, err := s.Reveal(console, item.ID, nil, "pw"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Reveal(api, item.ID, nil, ""); !errors.Is(err, ErrReauth) {
		t.Fatalf("the API does not share the console's window, got %v", err)
	}
}

func TestCollectionKeyRotationFailsClosed(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	_ = st.Storage().Update(func(tx kv.Tx) error {
		ver, _ := getVersionTx(tx, item.ID, 1)
		ver.CT = append([]byte{0}, ver.CT...)
		b, _ := json.Marshal(ver)
		return tx.Put(bucketVersions, versionRowKey(item.ID, 1), b)
	})
	if err := s.RotateCollectionKey(col.Name); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); s.rotatingNow(col.ID); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the rotation did not finish")
		}
	}
	if after, _ := s.Collection(col.Name); after.KeyVersion != 1 {
		t.Fatalf("a rotation that cannot open a version must roll back, key version %d", after.KeyVersion)
	}
}
