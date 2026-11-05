package main

// The backup command line: now (a backup at once, through the running service when it can, on
// its own otherwise), ls (what every destination holds), restore (decrypt, verify, put back),
// drill (prove the chain somewhere else and report the drill), keygen (a key pair and the
// recovery material for a deployment that wants its own keys). Everything here is an
// operator's tool: plain English output, secrets only where a hand-off needs them.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/openlinkos/beacon/internal/backup"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/version"
)

const backupUsage = `backup now [-config PATH]                     one backup at once (everything it is due)
backup ls [-config PATH]                       what every destination holds, newest first
backup restore FILE... -identity KEY [-into DIR] [-dry-run] [-confirm] [-verify-key KEY]
                                              decrypt, verify, put the database, recordings,
                                              configuration and secrets back; -dry-run verifies
                                              in a temporary directory and touches nothing;
                                              restoring into the live data directory needs -confirm
backup drill FILE... -identity KEY [-keep]     restore to a scratch directory, verify, report
                                              the drill to the running service when reachable
backup keygen                                  a key pair; the private key prints once, alone`

func backupCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, backupUsage)
		return 2
	}
	switch args[0] {
	case "now":
		return backupNow(args[1:], stdout, stderr)
	case "ls":
		return backupList(args[1:], stdout, stderr)
	case "restore":
		return backupRestore(args[1:], stdout, stderr)
	case "drill":
		return backupDrill(args[1:], stdout, stderr)
	case "keygen":
		return backupKeygen(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, backupUsage)
		return 2
	}
}

// backupSocket calls the running service's admin socket with the same data directory the
// configuration names. ok is false when there is nothing to reach (the service is stopped, or
// this is Windows, which has no socket): the caller falls back to running alone.
func backupSocket(cfg *config.Config, method, path string, in, out any) (ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute) // a backup may take a while
	defer cancel()
	var body io.Reader
	if in != nil {
		if b, err := json.Marshal(in); err == nil {
			body = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://beacon"+path, body)
	if err != nil {
		return false, err
	}
	req.Header.Set(ctl.UserHeader, ctl.CurrentUser())
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", ctl.Path(cfg.DataDir))
		},
	}}
	resp, err := hc.Do(req)
	if err != nil {
		return false, nil // unreachable is not an error here: the caller decides
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error != "" {
			return true, fmt.Errorf("%s", e.Error)
		}
		return true, fmt.Errorf("%s", resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return true, err
		}
	}
	return true, nil
}

func backupNow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backup now", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "configuration file")
	full := fs.Bool("full", false, "take a complete recordings set, not only what is due")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "backup now [-config PATH] [-full]")
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !cfg.Backup.Enabled() {
		fmt.Fprintln(stderr, "backups are off: the configuration names no destination")
		return 1
	}
	var out struct {
		Kind       string    `json:"kind"`
		Created    time.Time `json:"created"`
		Members    int       `json:"members"`
		Recordings int       `json:"recordings"`
		AuditHead  string    `json:"audit_head"`
	}
	if ok, err := backupSocket(cfg, http.MethodPost, "/v1/backup/run", map[string]any{}, &out); ok {
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "backup taken by the running service: %s at %s, %d members, %d recordings\n",
			out.Kind, out.Created.Local().Format(time.RFC3339), out.Members, out.Recordings)
		return 0
	}
	mgr, err := backupManager(cfg, *cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	man, err := mgr.Run(context.Background(), "manual", ctl.CurrentUser(), true, *full)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "backup taken: %s at %s, %d members, %d recordings (the service is not running; no audit entry)\n",
		man.Kind, man.Created.Local().Format(time.RFC3339), len(man.Members), man.Recordings)
	return 0
}

func backupList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backup ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "configuration file")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "backup ls [-config PATH]")
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	dsts, err := backupDestinations(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(dsts) == 0 {
		fmt.Fprintln(stdout, "backups are off: the configuration names no destination")
		return 0
	}
	empty := true
	for _, d := range dsts {
		entries, err := d.List(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", d.Name, err)
			continue
		}
		fmt.Fprintf(stdout, "%s:\n", d.Name)
		for _, e := range entries {
			empty = false
			fmt.Fprintf(stdout, "  %s  %s  %9d bytes  %s\n",
				e.When.Local().Format("2006-01-02 15:04"), e.Kind, e.Size, e.Name)
		}
		if len(entries) == 0 {
			fmt.Fprintf(stdout, "  (no backups yet)\n")
		}
	}
	if empty {
		return 1
	}
	return 0
}

func backupRestore(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "configuration file")
	identity := fs.String("identity", "", "the private key (an age key, or @FILE holding it or an ssh-ed25519 key)")
	verifyKey := fs.String("verify-key", "", "the manifest signing public key (a sign1:… line, or @FILE); default: from -identity")
	into := fs.String("into", "", "the directory to restore into (default: the configured data_dir)")
	dry := fs.Bool("dry-run", false, "verify in a temporary directory and change nothing")
	confirm := fs.Bool("confirm", false, "required when restoring into the live data directory")
	pos, ok := positional(fs, args)
	if !ok || len(pos) == 0 || *identity == "" {
		fmt.Fprintln(stderr, "backup restore FILE... -identity KEY|@FILE [-into DIR] [-dry-run] [-confirm] [-verify-key KEY|@FILE]")
		return 2
	}
	ids, identityBytes, err := loadIdentitiesRaw(*identity)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	verify, err := loadVerifyKey(*verifyKey, identityBytes)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	dir := *into
	if dir == "" {
		dir = cfg.DataDir
	}
	live := !*dry && backup.SameDataDir(dir, cfg.DataDir)
	if *dry {
		tmp, err := os.MkdirTemp("", "beacon-restore-")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		dir = filepath.Join(tmp, "restored")
		live = false
	} else if err := checkServiceStopped(cfg, stderr); err != nil {
		return 1
	}
	res, err := backup.Restore(context.Background(), backup.RestoreOpts{
		Paths: pos, Identities: ids, Verify: verify, Dir: dir, Live: live, Confirm: *confirm,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, m := range res.Manifests {
		fmt.Fprintln(stdout, backup.Describe(m))
	}
	if *dry {
		fmt.Fprintf(stdout, "verified in %s: the database and its audit chain hold, every member matches the manifest\n", dir)
		return 0
	}
	if res.Aside != "" {
		fmt.Fprintf(stdout, "live data copied aside: %s\n", res.Aside)
	}
	// place the database where the service opens it, through a temporary, reporting WAL failures
	db := filepath.Join(dir, "db", "beacon.db")
	if _, err := os.Stat(db); err == nil {
		if err := backup.PlaceDatabase(db, dir); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "database: %s\n", filepath.Join(dir, "beacon.db"))
	}
	fmt.Fprintf(stdout, "recordings: %s\n", filepath.Join(dir, "recordings"))
	fmt.Fprintf(stdout, "configuration: %s (place it as the deployment's files)\n", filepath.Join(dir, "config"))
	fmt.Fprintf(stdout, "secrets: %s (place it as the deployment's secrets directory)\n", filepath.Join(dir, "secrets"))
	return 0
}

// checkServiceStopped refuses to restore over a running service: it holds the database's lock
// file, and a restore under it would be lost on the next checkpoint.
func checkServiceStopped(cfg *config.Config, stderr io.Writer) error {
	db := cfg.Storage.Path
	if db == "" {
		db = filepath.Join(cfg.DataDir, "beacon.db")
	}
	if backup.ServiceRunning(db) {
		fmt.Fprintf(stderr, "the service is running (it holds %s.lock); stop it before restoring\n", db)
		return fmt.Errorf("the service is running")
	}
	return nil
}

func backupDrill(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backup drill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "configuration file")
	identity := fs.String("identity", "", "the private key (an age key, or @FILE holding it or an ssh-ed25519 key)")
	verifyKey := fs.String("verify-key", "", "the manifest signing public key (a sign1:… line, or @FILE); default: from -identity")
	keep := fs.Bool("keep", false, "keep the restored directory and print where")
	pos, ok := positional(fs, args)
	if !ok || len(pos) == 0 || *identity == "" {
		fmt.Fprintln(stderr, "backup drill FILE... -identity KEY|@FILE [-keep] [-verify-key KEY|@FILE]")
		return 2
	}
	ids, identityBytes, err := loadIdentitiesRaw(*identity)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	verify, err := loadVerifyKey(*verifyKey, identityBytes)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	res, took, err := backup.Drill(context.Background(), pos, ids, verify, *keep)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, m := range res.Manifests {
		fmt.Fprintln(stdout, backup.Describe(m))
	}
	fmt.Fprintf(stdout, "drill passed in %s: restored, members matched, integrity and audit chain verified\n", took.Round(time.Millisecond))
	if *keep {
		fmt.Fprintf(stdout, "restored into %s\n", res.Dir)
	}
	// the drill's report: to the running service when reachable, else to the state the next
	// start reads, so the recovery-drill resource renews either way
	cfg, cfgErr := config.Load(*cfgPath)
	if cfgErr != nil {
		fmt.Fprintf(stdout, "the drill time is not recorded: the configuration could not be read (%v); run this on the server, or record the drill there\n", cfgErr)
		return 0
	}
	if ok, err := backupSocket(cfg, http.MethodPost, "/v1/backup/drill",
		map[string]any{"drill": time.Now().UTC().Format(time.RFC3339)}, nil); ok && err == nil {
		fmt.Fprintln(stdout, "the drill is recorded with the running service")
		return 0
	}
	if err := backup.RecordDrill(cfg.DataDir, time.Now()); err != nil {
		fmt.Fprintf(stdout, "the drill time could not be recorded (%v); the running service is not reachable and its data directory is not here\n", err)
		return 0
	}
	fmt.Fprintln(stdout, "the drill time is recorded in the data directory; the running service (if any) does not know it yet")
	return 0
}

func backupKeygen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backup keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "backup keygen")
		return 2
	}
	k, err := backup.GenerateKey()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, k.Private) // the secret, alone on stdout, for a script to take; shown once
	fmt.Fprint(stderr, backup.RecoveryInstructions(k.Public))
	return 0
}

// loadIdentitiesRaw takes -identity: a literal AGE-SECRET-KEY or ssh key, or @FILE. The bytes
// are kept so a recovery-material file can also yield the manifest signing public key.
func loadIdentitiesRaw(s string) ([]age.Identity, []byte, error) {
	path, isFile := strings.CutPrefix(s, "@")
	if !isFile {
		if st, err := os.Stat(s); err == nil && !st.IsDir() {
			path, isFile = s, true
		}
	}
	if isFile {
		b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
		if err != nil {
			return nil, nil, err
		}
		ids, err := backup.ParseIdentities(b)
		return ids, b, err
	}
	ids, err := backup.ParseIdentities([]byte(s))
	return ids, []byte(s), err
}

func loadVerifyKey(flag string, identityBytes []byte) (ed25519.PublicKey, error) {
	if flag != "" {
		path, isFile := strings.CutPrefix(flag, "@")
		if !isFile {
			if st, err := os.Stat(flag); err == nil && !st.IsDir() {
				path, isFile = flag, true
			}
		}
		if isFile {
			return backup.LoadVerifyKey(path)
		}
		return backup.ParseSignPublic(flag)
	}
	pub, err := backup.ParseVerifyKey(identityBytes)
	if err != nil {
		return nil, err
	}
	if pub == nil {
		return nil, fmt.Errorf("a restore needs the manifest signing public key (in the recovery material, or -verify-key)")
	}
	return pub, nil
}

// backupDestinations is the configuration's destinations as the backup package's.
func backupDestinations(cfg *config.Config) ([]backup.Destination, error) {
	var out []backup.Destination
	for _, d := range cfg.Backup.Destinations {
		switch {
		case d.Dir != "":
			out = append(out, backup.Destination{Name: d.Dir, Dir: d.Dir})
		case d.S3 != nil:
			prefix := strings.TrimPrefix(d.S3.Prefix, "/")
			if prefix != "" && !strings.HasPrefix(prefix, "/") {
				prefix = "/" + prefix
			}
			name := fmt.Sprintf("s3://%s/%s%s", hostOf(d.S3.Endpoint), d.S3.Bucket, prefix)
			out = append(out, backup.Destination{Name: name, S3: &backup.S3{
				Endpoint: d.S3.Endpoint, Region: d.S3.Region, Bucket: d.S3.Bucket, Prefix: d.S3.Prefix,
				AccessKeyFile: d.S3.AccessKeyFile, SecretKeyFile: d.S3.SecretKeyFile,
			}})
		}
	}
	return out, nil
}

func hostOf(endpoint string) string {
	if i := strings.Index(endpoint, "://"); i > 0 {
		endpoint = endpoint[i+3:]
	}
	if i := strings.IndexAny(endpoint, "/"); i > 0 {
		endpoint = endpoint[:i]
	}
	return endpoint
}

// backupManager builds the manager from the configuration (and the file it came from, which
// travels in the backup), for the command line running on its own.
func backupManager(cfg *config.Config, cfgPath string) (*backup.Manager, error) {
	o, err := backupOptions(cfg, cfgPath, nil)
	if err != nil {
		return nil, err
	}
	return backup.New(o)
}

// backupOptions is the one place the configuration becomes the manager's options; audit is the
// store's audit when the service runs one. A bad recipient is a start-stopping error: a mistyped
// key must not quietly narrow who can read the backups.
func backupOptions(cfg *config.Config, cfgPath string, audit func(store.AuditEntry) error) (backup.Options, error) {
	recipients, err := backup.ParseRecipients(cfg.Backup.Recipients)
	if err != nil {
		return backup.Options{}, fmt.Errorf("backup.recipients: %w", err)
	}
	if cfg.Backup.RecipientsFile != "" {
		rs, err := backup.LoadRecipientsFile(cfg.Backup.RecipientsFile)
		if err != nil {
			return backup.Options{}, fmt.Errorf("backup.recipients_file: %w", err)
		}
		recipients = append(recipients, rs...)
	}
	recipients, generated, err := backup.EnsureKey(filepath.Join(cfg.DataDir, "backup"), recipients)
	if err != nil {
		return backup.Options{}, err
	}
	if generated {
		material := filepath.Join(cfg.DataDir, "backup", "RECOVERY-MATERIAL.txt")
		if _, err := os.Stat(filepath.Join(cfg.DataDir, "backup", "age-public.key")); err == nil {
			fmt.Fprintf(os.Stderr, "backup: no recipients were configured, so a key pair was generated; the recovery material is in %s — take it off this machine\n", material)
		} else {
			fmt.Fprintf(os.Stderr, "backup: the manifest signing public key is in %s — keep it with the identities backups encrypt to, off this machine\n", material)
		}
	}
	host, err := backup.ResolveInstanceName(cfg.DataDir, cfg.Backup.Host)
	if err != nil {
		return backup.Options{}, err
	}
	dsts, err := backupDestinations(cfg)
	if err != nil {
		return backup.Options{}, err
	}
	db := cfg.Storage.Path
	if db == "" {
		db = filepath.Join(cfg.DataDir, "beacon.db")
	}
	return backup.Options{
		Host: host, Version: version.Version, Driver: cfg.Storage.Driver, DBPath: db, DataDir: cfg.DataDir,
		ConfigFile: cfgPath, CatalogDir: cfg.CatalogDir,
		TargetsDir: cfg.Gateway.TargetsDir, ModulesFile: cfg.Gateway.ModulesFile,
		SecretsDir:    cfg.Backup.SecretsDir,
		Recipients:    recipients,
		Destinations:  dsts,
		ScheduleDB:    cfg.Backup.Schedule.Database.D(),
		ScheduleFiles: cfg.Backup.Schedule.Files.D(),
		ScheduleFull:  cfg.Backup.Schedule.FilesFull.D(),
		Retention: backup.Keep{
			Hourly: cfg.Backup.Retention.Hourly, Daily: cfg.Backup.Retention.Daily,
			Weekly: cfg.Backup.Retention.Weekly, Monthly: cfg.Backup.Retention.Monthly,
		},
		Retries: cfg.Backup.Retries, RetryWait: cfg.Backup.RetryWait.D(),
		VerifyReadback:  cfg.Backup.VerifyReadbackOn(),
		HeartbeatMaxAge: cfg.Backup.Heartbeat.MaxAge.D(), HeartbeatSeverity: cfg.Backup.Heartbeat.Severity,
		DrillExpiry: cfg.Backup.DrillExpiry.D(), VMURL: cfg.VictoriaMetrics.URL,
		OnFailure: cfg.Backup.OnFailure, Audit: audit,
	}, nil
}

// backupCatalogView wraps the catalogue with the backup's built-ins when backups are on: the
// beacon-backup heartbeat and the recovery-drill resource, which the catalogue's metrics then
// carry to the same rules as everything else.
func backupCatalogView(cfg *config.Config, cfgPath string, mgr *backup.Manager, cat func() *catalog.Catalog) func() *catalog.Catalog {
	if mgr == nil {
		return cat
	}
	return func() *catalog.Catalog { return mgr.Builtins(cat()) }
}
