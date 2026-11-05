// Package backup is Beacon's own backups (the plan's §11): an online-consistent snapshot of
// the SQL database (VACUUM INTO), the session recordings (incrementally), the configuration and
// the secrets directory, packed as one tar, zstd-compressed, age-encrypted to public keys the
// server holds — Beacon cannot read what it wrote — and written to every destination at once
// (local directories and S3-compatible stores), read back and compared before it counts as
// done. Grandfather–father–son retention prunes each destination; failures retry with growing
// waits and then go quiet on the built-in beacon-backup heartbeat, so a broken backup reaches
// people through the same HeartbeatStale alert as any other job that stopped reporting.
package backup

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/openlinkos/beacon/internal/store"
)

// Options is a configured manager, built from the configuration's backup section (and the paths
// around it) once, at start. Everything here is bootstrap-level on purpose: the recipients, the
// destinations and their credentials must be knowable before the database is open, because a
// restore has to reach its destination with no database to read settings from.
type Options struct {
	Host        string
	Version     string // the running Beacon, for manifests and the upgrade trigger
	Driver      string // sqlite (the default); postgres is refused until it has its own snapshot
	DBPath      string // the SQL database; a pre-migration run points at the bbolt file with Bbolt
	Bbolt       bool   // DBPath is the old bbolt file (the migration's before-backup)
	DataDir     string // <data>/backup holds the state and the recovery material
	ConfigFile  string
	CatalogDir  string // as the configuration names them; packed under config/
	TargetsDir  string
	ModulesFile string
	SecretsDir  string // packed whole, in every archive: a database snapshot and its secrets belong together

	Recipients   []age.Recipient
	Destinations []Destination

	ScheduleDB     time.Duration // the database alone (1h)
	ScheduleFiles  time.Duration // recordings, configuration, secrets (24h)
	ScheduleFull   time.Duration // a full recordings set every so often (7d)
	Retention      Keep
	Retries        int
	RetryWait      time.Duration // the first wait; it grows fourfold each retry
	VerifyReadback bool

	HeartbeatMaxAge   time.Duration
	HeartbeatSeverity string
	DrillExpiry       time.Duration
	VMURL             string                       // where the heartbeat sample is pushed
	OnFailure         string                       // alert (the default) or alert_and_pause
	Audit             func(store.AuditEntry) error // nil outside the service
	Log               *slog.Logger
	Now               func() time.Time
}

// State is what survives a restart, in <data>/backup/state.json. Watermark is how far the
// recordings have been packed (the increment's cut); Version is the Beacon that last started
// (the upgrade backup's trigger); LastDrill feeds the recovery-drill resource's expiry.
type State struct {
	Version    string    `json:"version"`
	Enabled    time.Time `json:"enabled"`
	Watermark  time.Time `json:"recordings_watermark"`
	NextDB     time.Time `json:"next_database"`
	NextFiles  time.Time `json:"next_files"`
	NextFull   time.Time `json:"next_full"`
	LastBackup time.Time `json:"last_backup"`
	LastDrill  time.Time `json:"last_drill"`
	Failures   int       `json:"failures,omitempty"`
	RetryAfter time.Time `json:"retry_after,omitempty"`
}

// Manager runs the backups: one at a time (a lock of its own), on its schedules and on demand.
type Manager struct {
	opts   Options
	dir    string // <data>/backup
	log    *slog.Logger
	now    func() time.Time
	signer ed25519.PrivateKey

	mu    sync.Mutex
	state State
	busy  bool
}

// New reads (or begins) the state beside the data directory and returns the manager.
func New(o Options) (*Manager, error) {
	if postgresDriver(o.Driver) {
		return nil, fmt.Errorf("backup: a PostgreSQL database cannot be snapshotted yet; turn backups off or keep storage.driver as sqlite")
	}
	if len(o.Destinations) == 0 {
		return nil, fmt.Errorf("backup: no destinations")
	}
	if len(o.Recipients) == 0 {
		return nil, fmt.Errorf("backup: no recipients (generate a key pair with `beacon backup keygen`)")
	}
	if o.Now == nil {
		o.Now = timeNow
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	m := &Manager{opts: o, dir: filepath.Join(o.DataDir, "backup"), log: o.Log, now: o.Now}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return nil, err
	}
	signer, err := EnsureSignKey(m.dir)
	if err != nil {
		return nil, err
	}
	m.signer = signer
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func postgresDriver(d string) bool {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "postgres", "postgresql":
		return true
	default:
		return false
	}
}

// RecoveryFile is where the one-time recovery material waits for its owner; PublicKeyFile is
// the public half the server keeps.
func (m *Manager) RecoveryFile() string { return filepath.Join(m.dir, "RECOVERY-MATERIAL.txt") }
func (m *Manager) publicFile() string   { return filepath.Join(m.dir, "age-public.key") }
func (m *Manager) stateFile() string    { return filepath.Join(m.dir, "state.json") }

// EnabledKey reports whether the manager generated its own key pair (the recovery material's
// warning is only for that case).
func (m *Manager) GeneratedKey() bool {
	_, err := os.Stat(m.publicFile())
	return err == nil
}

// load reads the state, writing the first one when there is none.
func (m *Manager) load() error {
	b, err := os.ReadFile(m.stateFile())
	if os.IsNotExist(err) {
		now := m.now().UTC()
		m.state = State{Version: m.opts.Version, Enabled: now,
			NextDB: now.Add(m.opts.ScheduleDB), NextFiles: now.Add(m.opts.ScheduleFiles)}
		return m.save()
	}
	if err != nil {
		return err
	}
	var s State
	if err := jsonUnmarshal(b, &s); err != nil {
		return fmt.Errorf("backup state %s: %w", m.stateFile(), err)
	}
	m.state = s
	return nil
}

func (m *Manager) save() error {
	return writeFileAtomic(m.stateFile(), jsonMarshalIndent(m.state))
}

// Run performs one backup now, whatever the schedules say. files decides whether the recordings
// and the configuration come too (the daily run) or only the database (the hourly one); full
// forces a complete recordings set; trigger names the cause and actor who for the audit log
// (schedule/upgrade/migration with actor system, manual with the person who asked).
func (m *Manager) Run(ctx context.Context, trigger, actor string, files, full bool) (Manifest, error) {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return Manifest{}, fmt.Errorf("a backup is already running")
	}
	m.busy = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.busy = false
		m.mu.Unlock()
	}()

	man, err := m.build(ctx, trigger, files, full)
	if err != nil {
		m.noteFailure(actor, trigger, err)
		return man, err
	}
	m.audit(store.AuditEntry{Actor: actorOrSystem(actor), Via: "system", Action: "backup.done",
		Target: man.Kind + " " + trigger,
		Detail: fmt.Sprintf("%d members, %d recordings, audit head %s", len(man.Members), man.Recordings, short(man.AuditHead))})
	m.pushHeartbeat(ctx)
	return man, nil
}

func failBackoff(first time.Duration, n int) time.Duration {
	if first <= 0 {
		first = time.Minute
	}
	wait := first
	for i := 1; i < n && i < 8; i++ {
		if wait > time.Hour/4 {
			return time.Hour
		}
		wait *= 4
	}
	if wait > time.Hour {
		return time.Hour
	}
	return wait
}

func (m *Manager) noteFailure(actor, trigger string, err error) {
	m.mu.Lock()
	m.state.Failures++
	n := m.state.Failures
	wait := failBackoff(m.opts.RetryWait, n)
	m.state.RetryAfter = m.now().UTC().Add(wait)
	_ = m.save()
	m.mu.Unlock()
	if n == 1 {
		m.audit(store.AuditEntry{Actor: actorOrSystem(actor), Via: "system", Action: "backup.failed",
			Target: trigger, Detail: err.Error()})
		return
	}
	m.log.Error("backup failed", "trigger", trigger, "attempt", n, "retry_after", wait.String(), "err", err)
}

// build is the backup itself: snapshot, check, pack, upload, read back, retain, record state.
func (m *Manager) build(ctx context.Context, trigger string, files, full bool) (Manifest, error) {
	started := m.now().UTC()
	tmp, err := os.MkdirTemp(m.dir, ".run-")
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	man := Manifest{Format: FormatVersion, DataVersion: DataVersion, Beacon: m.opts.Version, Host: m.opts.Host, Created: started}

	// the database: a consistent snapshot, checked before it is encrypted
	var sources []Source
	if m.opts.Bbolt {
		src := filepath.Join(tmp, "bbolt.db")
		if err := copyFile(m.opts.DBPath, src); err != nil {
			return Manifest{}, err
		}
		sources = append(sources, Source{Name: "db/status.db", Path: src})
		man.Note = "the bbolt database, before the SQL migration"
	} else {
		snap := filepath.Join(tmp, "beacon.db.snapshot")
		schema, tables, err := Snapshot(ctx, m.opts.DBPath, snap)
		if err != nil {
			return Manifest{}, err
		}
		if err := IntegrityCheck(ctx, snap); err != nil {
			return Manifest{}, fmt.Errorf("the snapshot failed its integrity check: %w", err)
		}
		res, err := VerifyAudit(ctx, snap)
		if err != nil {
			return Manifest{}, fmt.Errorf("the snapshot's audit chain does not verify: %w", err)
		}
		man.Schema, man.Tables, man.AuditHead, man.AuditSeq = schema, tables, res.Head, res.LastSeq
		sources = append(sources, Source{Name: "db/beacon.db", Path: snap})
	}

	// the secrets travel with every archive: a snapshot and the keys it was sealed with belong
	// together, whatever the schedules were doing when it was taken
	secrets, _, err := walkFiles(m.opts.SecretsDir)
	if err != nil {
		return Manifest{}, err
	}
	for _, s := range secrets {
		s.Name = "secrets/" + s.Name
		sources = append(sources, s)
	}

	// the recordings and the configuration: the daily set. Database-only hourly runs are kind
	// db, not incr: GFS over a shared incr kind kept the newest of the day (the hourly) and
	// dropped the recordings.
	kind := KindDB
	if files {
		m.mu.Lock()
		watermark, nextFull := m.state.Watermark, m.state.NextFull
		m.mu.Unlock()
		kind = KindIncr
		if full || watermark.IsZero() || !nextFull.After(started) {
			kind = KindFull
		}
		recs, mods, err := walkFiles(filepath.Join(m.opts.DataDir, "recordings"))
		if err != nil {
			return Manifest{}, err
		}
		for _, r := range recs {
			if kind == "incr" && !mods[r.Name].After(watermark) {
				continue
			}
			r.Name = "recordings/" + r.Name
			sources = append(sources, r)
		}
		sources = append(sources, m.configSources(tmp)...)
	}

	// the archive. The name carries the second; a second backup inside the same second takes
	// the next free second rather than overwriting the first.
	man.Kind = kind
	name := ArchiveName(m.opts.Host, started, kind)
	for bump := 0; m.nameTaken(ctx, name); bump++ {
		if bump >= 5 {
			return Manifest{}, fmt.Errorf("every name near %s is taken", name)
		}
		started = started.Add(time.Second)
		man.Created = started
		name = ArchiveName(m.opts.Host, started, kind)
	}
	archive := filepath.Join(tmp, name)
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // inside the run's own temporary directory
	if err != nil {
		return Manifest{}, err
	}
	written, err := Create(f, m.opts.Recipients, m.signer, sources, man)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Manifest{}, err
	}
	st, err := os.Stat(archive)
	if err != nil {
		return Manifest{}, err
	}
	h, err := hashFile(archive)
	if err != nil {
		return Manifest{}, err
	}
	m.log.Info("backup built", "name", name, "kind", kind, "trigger", trigger,
		"members", len(written.Members), "recordings", written.Recordings, "bytes", st.Size())

	// every destination, read back, then retention
	for _, dst := range m.opts.Destinations {
		if err := m.put(ctx, dst, name, archive, st.Size(), h); err != nil {
			return written, fmt.Errorf("destination %s: %w", dst.Name, err)
		}
	}
	if err := m.prune(ctx); err != nil {
		m.log.Warn("retention could not run", "err", err) // kept over failing the backup for it
	}

	// state: the watermark covers what this run packed
	m.mu.Lock()
	m.state.LastBackup = started
	m.state.Failures = 0
	m.state.RetryAfter = time.Time{}
	if files {
		m.state.Watermark = started
		if kind == KindFull {
			m.state.NextFull = started.Add(m.opts.ScheduleFull)
		}
	}
	m.state.NextDB = started.Add(m.opts.ScheduleDB)
	if files {
		m.state.NextFiles = started.Add(m.opts.ScheduleFiles)
	}
	err = m.save()
	m.mu.Unlock()
	return written, err
}

// nameTaken reports whether any destination already holds a backup of that name.
func (m *Manager) nameTaken(ctx context.Context, name string) bool {
	for _, dst := range m.opts.Destinations {
		entries, err := dst.List(ctx)
		if err != nil {
			continue // a destination that cannot be asked is not allowed to block the name
		}
		for _, e := range entries {
			if e.Name == name {
				return true
			}
		}
	}
	return false
}

// put uploads to one destination and reads it back: size and hash both, as configured (on by
// default). A failure after a partial write cleans up after itself where it can.
func (m *Manager) put(ctx context.Context, dst Destination, name, path string, size int64, hash string) error {
	var lastErr error
	for attempt := 0; attempt <= m.opts.Retries; attempt++ {
		if attempt > 0 {
			wait := m.opts.RetryWait * (1 << (2 * min(attempt-1, 4))) // 1×, 4×, 16× …
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			m.log.Warn("backup retrying", "destination", dst.Name, "attempt", attempt+1, "wait", wait.String(), "err", lastErr.Error())
		}
		f, err := os.Open(path) //nolint:gosec // the run's own archive
		if err != nil {
			return err
		}
		_, err = dst.Put(ctx, name, f, size)
		_ = f.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if m.opts.VerifyReadback {
			n, h, err := dst.ReadBack(ctx, name)
			if err != nil {
				lastErr = err
				continue
			}
			if n != size || h != hash {
				lastErr = fmt.Errorf("read back %d bytes (hash %s), wrote %d (hash %s)", n, short(h), size, short(hash))
				continue
			}
		}
		return nil
	}
	_ = dst.Delete(ctx, name)
	return fmt.Errorf("gave up after %d tries: %w", m.opts.Retries+1, lastErr)
}

// prune applies the retention at every destination, only to this host's backups (a shared
// destination must not have one Beacon delete another's). An empty S3 prefix is handled by
// List, which refuses keys with extra path components.
func (m *Manager) prune(ctx context.Context) error {
	now := m.now()
	for _, dst := range m.opts.Destinations {
		entries, err := dst.List(ctx)
		if err != nil {
			return fmt.Errorf("list %s: %w", dst.Name, err)
		}
		var list []Backup
		for _, e := range entries {
			host, _, _, ok := ParseName(e.Name)
			if !ok {
				continue
			}
			if m.opts.Host != "" && host != m.opts.Host {
				continue
			}
			list = append(list, Backup{Name: e.Name, Time: e.When, Kind: e.Kind, Host: host})
		}
		keep, skew := Plan(list, m.opts.Retention, now)
		if skew {
			m.log.Error("backup retention: the clock is behind some backups; future-dated archives are kept",
				"destination", dst.Name, "now", now.UTC().Format(time.RFC3339))
			m.audit(store.AuditEntry{Actor: "system", Via: "system", Action: "backup.retention_skipped",
				Target: dst.Name, Detail: "the clock is behind some backups; future-dated archives are kept"})
		}
		for _, b := range list {
			if keep[b.Name] {
				continue
			}
			if err := dst.Delete(ctx, b.Name); err != nil {
				return fmt.Errorf("delete %s from %s: %w", b.Name, dst.Name, err)
			}
			m.log.Info("backup pruned", "name", b.Name, "destination", dst.Name)
		}
	}
	return nil
}

// configSources is the configuration as members, under config/: the running config.yaml, the
// catalogue, the probe targets and (when it lives outside the targets directory) the modules
// file. The secrets directory is not packed here — it travels with every archive, above.
func (m *Manager) configSources(tmp string) []Source {
	var out []Source
	if f := m.opts.ConfigFile; f != "" {
		if _, err := os.Stat(f); err == nil {
			out = append(out, Source{Name: "config/config.yaml", Path: f})
		}
	}
	if files, _, err := walkFiles(m.opts.CatalogDir); err == nil {
		for _, f := range files {
			f.Name = "config/catalog/" + f.Name
			out = append(out, f)
		}
	}
	covered := false
	if files, _, err := walkFiles(m.opts.TargetsDir); err == nil {
		for _, f := range files {
			f.Name = "config/targets/" + f.Name
			out = append(out, f)
			if f.Path == m.opts.ModulesFile {
				covered = true
			}
		}
	}
	if m.opts.ModulesFile != "" && !covered {
		if _, err := os.Stat(m.opts.ModulesFile); err == nil {
			// a modules file outside the targets directory keeps its own name beside them
			dst := filepath.Join(tmp, "modules.yml")
			if err := copyFile(m.opts.ModulesFile, dst); err == nil {
				out = append(out, Source{Name: "config/modules.yml", Path: dst})
			}
		}
	}
	return out
}

// audit records to the audit log when the manager runs inside the service.
func (m *Manager) audit(e store.AuditEntry) {
	if m.opts.Audit == nil {
		return
	}
	e.Time = m.now().UTC()
	if err := m.opts.Audit(e); err != nil {
		m.log.Warn("the backup's audit entry could not be written", "action", e.Action, "err", err)
	}
}

// pushHeartbeat reports the backup's success the way every heartbeat job does: one sample,
// pushed to the metrics store, that the HeartbeatStale rule watches.
func (m *Manager) pushHeartbeat(ctx context.Context) {
	if m.opts.VMURL == "" {
		return
	}
	if err := pushHeartbeatSample(ctx, m.opts.VMURL, "beacon-backup", m.now()); err != nil {
		m.log.Warn("the backup heartbeat could not be pushed", "err", err)
	}
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// ServiceRunning reports whether some process holds the database's lock file — the service,
// when it is up. A restore checks this before replacing anything: a database written under a
// running service would be lost on its next checkpoint.
func ServiceRunning(dbPath string) bool {
	release, err := lockExclusive(dbPath + ".lock")
	if err != nil {
		return true // the lock is held (or cannot be taken): either way, do not touch it
	}
	release()
	return false
}

// ---- the recovery material, as the console shows it

// RecoveryMaterial returns the one-time recovery material while it is still waiting to be
// collected; ok is false once the operator has taken it off (or none was ever written, because
// the configuration named its own recipients).
func (m *Manager) RecoveryMaterial() (b []byte, ok bool) {
	b, err := os.ReadFile(m.RecoveryFile())
	if err != nil {
		return nil, false
	}
	return b, true
}

// ConsumeRecoveryMaterial returns the recovery material and deletes it: the first download is
// the last. The rename is the consume, so two requests cannot both read it.
func (m *Manager) ConsumeRecoveryMaterial() ([]byte, error) {
	path := m.RecoveryFile()
	taken := path + ".taken"
	if err := os.Rename(path, taken); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(taken) //nolint:gosec // just renamed off the waiting path
	if err != nil {
		_ = os.Rename(taken, path)
		return nil, err
	}
	_ = os.Remove(taken)
	return b, nil
}

// RemoveRecoveryMaterial deletes the recovery material: from then on the server holds only the
// public key, and the page cannot show what it no longer has.
func (m *Manager) RemoveRecoveryMaterial() error {
	err := os.Remove(m.RecoveryFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// PublicKey is the age public half the server keeps (empty when the configuration named its own
// recipients and none was generated here).
func (m *Manager) PublicKey() string {
	b, err := os.ReadFile(m.publicFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SignPublicKey is the manifest-signing public key a restore verifies with.
func (m *Manager) SignPublicKey() string {
	if len(m.signer) == ed25519.PrivateKeySize {
		return FormatSignPublic(signPublicOf(m.signer))
	}
	b, err := os.ReadFile(filepath.Join(m.dir, signPubFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// VerifyKey is the ed25519 public half of the server's signing key.
func (m *Manager) VerifyKey() ed25519.PublicKey {
	return signPublicOf(m.signer)
}
