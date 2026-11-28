package sshgate

import (
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/web/link"
)

type readCountingFiles struct {
	Files
	reads int
}

func (f *readCountingFiles) ReadFile(name string) ([]byte, error) {
	f.reads++
	return f.Files.ReadFile(name)
}

func (f *readCountingFiles) Open(name string) (io.ReadCloser, error) {
	f.reads++
	return f.Files.Open(name)
}

func TestSessionReadersAuthoriseBeforeOpeningFiles(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	files := &readCountingFiles{Files: DirFiles{Dir: t.TempDir()}}
	g := &Gate{d: Deps{Store: st, Files: files, Pages: &link.Pages{PublicURL: "https://beacon.example.com"}}}
	epoch := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	register := func(name string, added time.Time, previous *store.Machine) store.Machine {
		t.Helper()
		m, err := st.RegisterMachine(store.Machine{Name: name, Added: added}, previous, "test")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	old := register("east", epoch.Add(-time.Hour), nil)
	current := register("east", epoch, &old)
	other := register("west", epoch, nil)
	retired := register("retired", epoch, nil)
	if err := st.DeleteMachine(retired.Name); err != nil {
		t.Fatal(err)
	}
	record := func(machine store.Machine, delta time.Duration, legacy bool) store.SessionRecord {
		t.Helper()
		when := epoch.Add(delta)
		id := when.Format("20060102-150405") + "-abcdef-1"
		r := store.SessionRecord{ID: id, Agent: "alice / duty", Machine: machine.Name, Kind: "shell",
			Started: when, Ended: when.Add(time.Second), MachineRegistration: machine.Registration(),
			Command: "hostname", Recording: "2026/11/25/" + id}
		if legacy {
			r.MachineRegistration = ""
		}
		if err := st.PutSession(r); err != nil {
			t.Fatal(err)
		}
		if err := files.WriteFile(r.Recording+".cast", []byte("fixture recording")); err != nil {
			t.Fatal(err)
		}
		if err := files.WriteFile(r.Recording+".json", []byte(`{"stdout":"fixture transcript","page":"https://old.example.com/?s=fixture"}`)); err != nil {
			t.Fatal(err)
		}
		return r
	}
	available := record(current, time.Second, false)
	legacy := record(current, 2*time.Second, true)
	previous := record(old, 3*time.Second, false)
	preEnrolment := record(old, -time.Second, true)
	foreign := record(other, 4*time.Second, false)
	deleted := record(retired, 5*time.Second, false)
	scoped := store.Principal{User: "alice", Agent: "duty", Scopes: []string{"sessions:read"},
		Machines: []string{current.Name, retired.Name}}
	for _, denied := range []string{previous.ID, foreign.ID, deleted.ID, preEnrolment.ID, "absent"} {
		if _, err := g.SessionFor(scoped, denied); !errors.Is(err, ErrNoSession) {
			t.Errorf("transcript did not report a missing session: %s, %v", denied, err)
		}
		if file, err := g.RecordingFor(scoped, denied); !errors.Is(err, ErrNoSession) || file != nil {
			t.Errorf("recording did not report a missing session: %s, %v", denied, err)
		}
	}
	metadata := scoped
	metadata.Scopes = []string{"status:read"}
	if _, err := g.SessionFor(metadata, available.ID); !errors.Is(err, ErrNoSession) {
		t.Fatal("metadata permission exposed the transcript")
	}
	if _, err := g.RecordingFor(metadata, available.ID); !errors.Is(err, ErrNoSession) {
		t.Fatal("metadata permission exposed the recording")
	}
	if files.reads != 0 {
		t.Fatalf("denied requests opened %d recording objects", files.reads)
	}
	checkList := func(p store.Principal, f SessionFilter) []SessionView {
		t.Helper()
		views, err := g.SessionsFor(p, f)
		if err != nil {
			t.Fatal(err)
		}
		return views
	}
	full := checkList(scoped, SessionFilter{Agent: "duty", Limit: 2})
	if len(full) != 2 || full[0].ID != legacy.ID || full[1].ID != available.ID {
		t.Fatalf("pagination included hidden sessions or dropped permitted sessions: %+v", full)
	}
	if views := checkList(scoped, SessionFilter{Machine: other.Name}); len(views) != 0 {
		t.Fatal("a user-selected filter widened the machine grant")
	}
	if views := checkList(metadata, SessionFilter{}); len(views) != 2 ||
		views[0].Command != "" || views[0].Recording != "" || views[0].Page != "" {
		t.Fatalf("metadata-only session view disclosed details: %+v", views)
	}
	wantPage := "https://beacon.example.com" + link.SignInPath(link.SessionPath(available.ID))
	if full[1].Command != available.Command || full[1].Page != wantPage {
		t.Fatalf("missing command or ordinary sign-in URL: %+v", full[1])
	}
	got, err := g.SessionFor(scoped, available.ID)
	if err != nil || got.Stdout != "fixture transcript" || got.Page != wantPage {
		t.Fatalf("permitted transcript or safe replay URL missing: %+v, %v", got, err)
	}
	if u, err := url.Parse(got.Page); err != nil || u.Query().Get(link.Param) != "" {
		t.Fatalf("the authenticated result issued a bearer link: %s", got.Page)
	}
	sshOnly := scoped
	sshOnly.Scopes = []string{"machines:ssh"}
	rc, err := g.RecordingFor(sshOnly, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil || string(body) != "fixture recording" {
		t.Fatalf("legacy recording permitted by the current enrolment: %q, %v", body, err)
	}
	person := store.Principal{User: "alice", Scopes: []string{"sessions:read"}, MachineAccess: store.MachineAccess{Access: store.AccessEvery}}
	if got, err := g.SessionFor(person, previous.ID); err != nil || got.ID != previous.ID {
		t.Fatalf("the retained historical transcript was hidden from the unscoped person: %s, %v", got.ID, err)
	}
	if got, err := g.Session(previous.ID); err != nil || got.ID != previous.ID {
		t.Fatalf("trusted replay could not read the retained transcript: %s, %v", got.ID, err)
	}
}
