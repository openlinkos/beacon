package backup

// The schedule and the pieces around it: the loop that wakes for the database's hourly run and
// the files' daily one (and remembers where it got to across restarts); the one-shot backups a
// version change and the bbolt migration trigger; the heartbeat sample a successful backup
// pushes; the built-ins the catalogue's metrics gain when backups are on (the beacon-backup
// heartbeat and the recovery-drill resource); the recovery material written exactly once when
// Beacon generates the key pair itself; and the local admin socket's endpoint, through which
// `beacon backup now` asks the running service.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"filippo.io/age"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/store"
)

// Start runs the schedules until ctx ends: the database alone every ScheduleDB, the recordings
// and configuration every ScheduleFiles, each remembered in the state so a restart neither
// repeats nor skips one quietly. A version change takes one extra files backup first (the
// plan's: before an upgrade), and the first ever run takes one at once — a deployment that
// turns backups on should not wait an hour for its first.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	firstRun := m.state.LastBackup.IsZero()
	upgrade := m.state.Version != m.opts.Version
	m.state.Version = m.opts.Version
	err := m.save()
	m.mu.Unlock()
	if err != nil {
		m.log.Warn("the backup state could not be written", "err", err)
	}

	run := func(trigger string, files bool) {
		if _, err := m.Run(ctx, trigger, "system", files, false); err != nil {
			m.log.Error("backup failed", "trigger", trigger, "err", err)
		}
	}
	switch {
	case firstRun:
		run("first", true)
	case upgrade:
		run("upgrade", true)
	}

	tick := time.NewTicker(time.Duration(schedTickNs.Load()))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now := m.now().UTC()
			m.mu.Lock()
			waiting := !m.state.RetryAfter.IsZero() && now.Before(m.state.RetryAfter)
			dbDue, filesDue := !m.state.NextDB.After(now), !m.state.NextFiles.After(now)
			m.mu.Unlock()
			if waiting {
				continue
			}
			if filesDue {
				run("schedule", true)
			} else if dbDue {
				run("schedule", false)
			}
		}
	}
}

// BackupBeforeMigration takes one backup of the old bbolt file before the SQL migration moves
// it (the plan's: before a migration). It runs with the service not yet up: no audit, no
// heartbeat, just the archive at every destination.
func BackupBeforeMigration(ctx context.Context, o Options) error {
	o.Bbolt = true
	o.Audit = nil
	m, err := New(o)
	if err != nil {
		return err
	}
	_, err = m.Run(ctx, "migration", "system", true, true)
	return err
}

// pushHeartbeatSample writes one sample of beacon_heartbeat_last_success_timestamp_seconds to
// the metrics store's import endpoint — the same push a heartbeat job's own script makes, so
// the HeartbeatStale rule watches Beacon's backups exactly as it watches any other job.
func pushHeartbeatSample(ctx context.Context, vmURL, name string, now time.Time) error {
	body := fmt.Sprintf("beacon_heartbeat_last_success_timestamp_seconds{heartbeat=%q} %d\n", name, now.Unix())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(vmURL, "/")+"/api/v1/import/prometheus", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("import: %s", resp.Status)
	}
	return nil
}

// HeartbeatName is the built-in heartbeat's name; ResourceDrill is the built-in resource that
// reminds on recovery drills.
const (
	HeartbeatName = "beacon-backup"
	ResourceDrill = "recovery-drill"
)

// schedTickNs is how often the schedules are looked at, in nanoseconds (an atomic so a test can
// hurry it while a scheduler runs); twenty seconds by default.
var schedTickNs atomic.Int64

func init() { schedTickNs.Store(int64(20 * time.Second)) }

// Builtins returns the catalogue with the backup's built-ins added, without touching the one
// the caller holds: the beacon-backup heartbeat (max_age from the options) and the
// recovery-drill resource, expiring one drill period after the last drill — or after backups
// were first turned on, so a deployment that never drills hears about it.
func (m *Manager) Builtins(c *catalog.Catalog) *catalog.Catalog {
	out := *c
	for _, h := range out.Heartbeats {
		if h.Name == HeartbeatName {
			return &out // the deployment named its own: its settings stand
		}
	}
	out.Heartbeats = append(append([]catalog.Heartbeat(nil), out.Heartbeats...), catalog.Heartbeat{
		Name:        HeartbeatName,
		MaxAge:      config.Duration(m.opts.HeartbeatMaxAge),
		Severity:    m.opts.HeartbeatSeverity,
		Description: "Beacon's own backups (built in)",
	})
	for _, r := range out.Resources {
		if r.Name == ResourceDrill {
			return &out
		}
	}
	m.mu.Lock()
	since := m.state.LastDrill
	if since.IsZero() {
		since = m.state.Enabled
	}
	m.mu.Unlock()
	if since.IsZero() {
		since = m.now()
	}
	out.Resources = append(append([]catalog.Resource(nil), out.Resources...), catalog.Resource{
		Name:    ResourceDrill,
		Kind:    "service",
		Expires: since.Add(m.opts.DrillExpiry).UTC().Format("2006-01-02"),
		Note:    "renewed by `beacon backup drill`; the recovery it proves is the backup's whole point",
	})
	// the catalogue parses its resource dates at validation; the built-ins arrive after the
	// files were validated, so the copy validates again (the built-ins must parse like the rest)
	if err := out.Validate(); err != nil {
		return c
	}
	return &out
}

// RecordDrill writes a successful drill's time straight into the data directory's state (the
// command line, when the service is not reachable): the running service reads it at its next
// start, and the recovery-drill resource's expiry follows it.
func RecordDrill(dataDir string, t time.Time) error {
	dir := filepath.Join(dataDir, "backup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "state.json")
	var s State
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // beside the database
		_ = jsonUnmarshal(b, &s)
	}
	if s.Enabled.IsZero() {
		s.Enabled = t.UTC()
	}
	s.LastDrill = t.UTC()
	return writeFileAtomic(path, jsonMarshalIndent(s))
}

// RecordDrill notes that a recovery drill succeeded at t (the state the resource's expiry
// reads) and audits it.
func (m *Manager) RecordDrill(t time.Time, by string) error {
	m.mu.Lock()
	m.state.LastDrill = t.UTC()
	err := m.save()
	m.mu.Unlock()
	m.audit(store.AuditEntry{Actor: actorOrSystem(by), Via: "cli", Action: "backup.drill", Target: ResourceDrill,
		Detail: "a backup was restored and verified in a drill"})
	return err
}

// LastDrill reports when the last drill was recorded (zero when never).
func (m *Manager) LastDrill() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.LastDrill
}

func actorOrSystem(by string) string {
	if by == "" {
		return "system"
	}
	return by
}

// EnsureKey resolves the recipients a backup encrypts to: the configuration's own list, and —
// when there is none — a key pair Beacon generates itself, once, with the private half written
// to the recovery material file for the owner to take away (the server keeps the public half
// only). The second return says the material is newly written and waiting to be collected.
func EnsureKey(dir string, fromConfig []age.Recipient) ([]age.Recipient, bool, error) {
	sign, err := EnsureSignKey(dir)
	if err != nil {
		return nil, false, err
	}
	signPub := FormatSignPublic(signPublicOf(sign))
	if len(fromConfig) > 0 {
		written, err := ensureVerifyKeyMaterial(dir, signPub)
		if err != nil {
			return nil, false, err
		}
		return fromConfig, written, nil
	}
	pub := filepath.Join(dir, "age-public.key")
	if b, err := os.ReadFile(pub); err == nil { //nolint:gosec // the key Beacon itself wrote
		rs, err := ParseRecipients(strings.Split(strings.TrimSpace(string(b)), "\n"))
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", pub, err)
		}
		if len(rs) > 0 {
			return rs, false, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	k, err := GenerateKey()
	if err != nil {
		return nil, false, err
	}
	host, _ := os.Hostname()
	if err := writeFileAtomic(filepath.Join(dir, "RECOVERY-MATERIAL.txt"), []byte(RecoveryMaterial(k, host, signPub))); err != nil {
		return nil, false, err
	}
	if err := writeFileAtomic(pub, []byte(k.Public+"\n")); err != nil {
		return nil, false, err
	}
	rs, err := ParseRecipients([]string{k.Public})
	if err != nil {
		return nil, false, err
	}
	return rs, true, nil
}

const recoveryMaterialName = "RECOVERY-MATERIAL.txt"

// ensureVerifyKeyMaterial writes the manifest signing public key into the recovery material
// even when the operator named their own recipients. A restore after the server is gone has
// only this file (and their identity) to check the archive with.
func ensureVerifyKeyMaterial(dir, signPub string) (written bool, err error) {
	path := filepath.Join(dir, recoveryMaterialName)
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the file Beacon itself wrote
		if pub, perr := ParseVerifyKey(b); perr == nil && pub != nil {
			return false, nil
		}
		if err := writeFileAtomic(path, append(append([]byte{}, b...), []byte("\n"+signPublicSection(signPub))...)); err != nil {
			return false, err
		}
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	host, _ := os.Hostname()
	if err := writeFileAtomic(path, []byte(SignPublicMaterial(host, signPub))); err != nil {
		return false, err
	}
	return true, nil
}

// ---- the admin socket's endpoint

// CTLHandler answers the local admin socket: POST /v1/backup/run runs a backup now, POST
// /v1/backup/drill records a drill that succeeded elsewhere. The socket itself is the service's
// own, restricted to its user; the user running the command goes to the audit log.
func CTLHandler(run func(ctx context.Context, trigger, by string) (Manifest, error),
	drill func(t time.Time, by string) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		by := r.Header.Get(ctl.UserHeader)
		var in struct {
			Drill time.Time `json:"drill,omitempty"`
		}
		if r.Body != nil {
			defer func() { _ = r.Body.Close() }()
			b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			if len(b) > 0 {
				if err := json.Unmarshal(b, &in); err != nil {
					http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
					return
				}
			}
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/drill"):
			if r.Method != http.MethodPost {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			if in.Drill.IsZero() {
				http.Error(w, `{"error":"the drill time is missing"}`, http.StatusBadRequest)
				return
			}
			if err := drill(in.Drill, by); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/run"):
			if r.Method != http.MethodPost {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			man, err := run(r.Context(), "manual", by)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"kind": man.Kind, "created": man.Created, "members": len(man.Members),
				"recordings": man.Recordings, "audit_head": man.AuditHead,
			})
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	})
}
