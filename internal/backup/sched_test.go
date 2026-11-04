package backup

// The pieces around the backups: the heartbeat sample a successful backup pushes, the admin
// socket's endpoint, and the schedule loop waking when a run is due.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
)

func TestHeartbeatSampleIsOnePush(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	when := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := pushHeartbeatSample(context.Background(), srv.URL, "beacon-backup", when); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("one push, %d arrived", len(bodies))
	}
	want := `beacon_heartbeat_last_success_timestamp_seconds{heartbeat="beacon-backup"} ` +
		strconv.FormatInt(when.Unix(), 10)
	if got := strings.TrimSpace(bodies[0]); got != want {
		t.Fatalf("the sample: %q, want %q", got, want)
	}
	if !strings.HasSuffix(bodies[0], "\n") {
		t.Fatalf("the sample does not end its line: %q", bodies[0])
	}
}

func TestCTLHandlerRunAndDrill(t *testing.T) {
	var ran int
	var drilled time.Time
	h := CTLHandler(
		func(ctx context.Context, trigger, by string) (Manifest, error) {
			ran++
			if by != "alice" {
				t.Errorf("the user header did not travel: %q", by)
			}
			return Manifest{Kind: "full", Created: time.Now().UTC(), Members: []Member{{Name: "db/beacon.db"}}}, nil
		},
		func(when time.Time, by string) error {
			drilled = when
			return nil
		})
	// a run, as the running service would take it
	req := httptest.NewRequest(http.MethodPost, "/v1/backup/run", nil)
	req.Header.Set("X-Status-User", "alice")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || ran != 1 {
		t.Fatalf("run: %d %s", ran, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"kind":"full"`) {
		t.Fatalf("the run's answer: %s", rec.Body.String())
	}
	// a drill's report
	when := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	req = httptest.NewRequest(http.MethodPost, "/v1/backup/drill",
		strings.NewReader(`{"drill":"`+when.Format(time.RFC3339)+`"}`))
	req.Header.Set("X-Status-User", "bob")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !drilled.Equal(when) {
		t.Fatalf("drill: %s %v", rec.Body.String(), drilled)
	}
	// nothing else answers
	for _, path := range []string{"/v1/backup/other", "/v1/backup"} {
		req = httptest.NewRequest(http.MethodPost, path, nil)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d", path, rec.Code)
		}
	}
}

func TestStartRunsWhenDue(t *testing.T) {
	dataDir := t.TempDir()
	destDir := t.TempDir()
	fixtureTree(t, dataDir)
	st := fixtureStore(t, filepath.Join(dataDir, "beacon.db"))
	_ = st.Close()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := New(testOptions(t, dataDir, destDir, []age.Recipient{id.Recipient()}))
	if err != nil {
		t.Fatal(err)
	}
	// the first run happens at start; then the schedule's turn is remembered, and the next
	// database-only run waits for its own hour, not a restart
	oldTick := schedTick
	schedTick = 20 * time.Millisecond
	t.Cleanup(func() { schedTick = oldTick })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go mgr.Start(ctx)
	waitFor(t, 5*time.Second, func() bool {
		entries, err := Destination{Dir: destDir}.List(context.Background())
		return err == nil && len(entries) >= 1
	})
	// the state now says when the next runs are, and they are not due at once
	mgr.mu.Lock()
	nextDB, nextFiles := mgr.state.NextDB, mgr.state.NextFiles
	mgr.mu.Unlock()
	if timeNow().After(nextDB) || timeNow().After(nextFiles) {
		t.Fatalf("the next runs read as due: db %v files %v now %v", nextDB, nextFiles, timeNow())
	}
}

func TestRecordDrillStandalone(t *testing.T) {
	dir := t.TempDir()
	when := time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)
	if err := RecordDrill(dir, when); err != nil {
		t.Fatal(err)
	}
	mgr, err := New(testOptions(t, dir, t.TempDir(), mustRecipient(t)))
	if err != nil {
		t.Fatal(err)
	}
	if !mgr.LastDrill().Equal(when) {
		t.Fatalf("the drill time did not survive the manager's start: %v", mgr.LastDrill())
	}
}

func mustRecipient(t *testing.T) []age.Recipient {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return []age.Recipient{id.Recipient()}
}

// waitFor polls what the schedule did, so the loop's own timing is not slept around.
func waitFor(t *testing.T, within time.Duration, what func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if what() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nothing happened in time")
}
