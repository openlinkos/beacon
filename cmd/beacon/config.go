package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/notify"
	"github.com/openlinkos/beacon/internal/store"
)

// The catalogue, probe targets and modules live in the database (package configdb). The files the
// configuration names (catalog_dir, gateway.targets_dir, gateway.modules_file) are imported once,
// into a database that was never configured; after that only on SIGHUP, when nothing was changed
// through Beacon since the last import, or with `beacon config import DIR`.

func language(cfg *config.Config) i18n.Lang {
	if cfg.Language == "en" {
		return i18n.EN
	}
	return i18n.ZH
}

func configFiles(cfg *config.Config) (configdb.Files, error) {
	return configdb.ReadFiles(cfg.CatalogDir, cfg.Gateway.TargetsDir, cfg.Gateway.ModulesFile)
}

// openConfig opens the configuration in the database, importing the files into a database that was
// never configured. Files that fail their checks then stop the start, as they always did.
func openConfig(ctx context.Context, cfg *config.Config, st *store.Store, bot *notify.GroupBot, log *slog.Logger) (*configdb.Service, error) {
	d := configdb.Deps{Store: st, Lang: language(cfg), Log: log}
	if bot != nil {
		d.Bot = bot
	}
	cdb := configdb.Open(d)
	files, err := configFiles(cfg)
	if err != nil {
		return nil, fmt.Errorf("reading the configuration files: %w", err)
	}
	where := []any{"catalog_dir", cfg.CatalogDir, "targets_dir", cfg.Gateway.TargetsDir, "modules_file", cfg.Gateway.ModulesFile}
	switch {
	case len(files) == 0:
	case cdb.Version() > 0:
		log.Info("the configuration is in the database; its files are imported only on SIGHUP or with beacon config import",
			append([]any{"version", cdb.Version()}, where...)...)
	default:
		ch, err := cdb.Import(ctx, configdb.Actor{Name: "beacon", Via: "start"}, files, "", false)
		if err != nil {
			return nil, fmt.Errorf("importing the configuration files (catalog_dir %s, gateway.targets_dir %s, gateway.modules_file %s): %w",
				cfg.CatalogDir, cfg.Gateway.TargetsDir, cfg.Gateway.ModulesFile, err)
		}
		log.Info("imported the configuration files into the database, where it is changed from now on",
			append([]any{"version", ch.Version, "items", len(ch.Items)}, where...)...)
	}
	return cdb, nil
}

// reloadOnHUP imports the configuration files when the process gets SIGHUP, as long as nothing was
// changed through Beacon since the last import. Otherwise, or when the files fail their checks, it
// says why and keeps the configuration in force.
func reloadOnHUP(ctx context.Context, ch <-chan os.Signal, cfg *config.Config, cdb *configdb.Service, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			files, err := configFiles(cfg)
			if err != nil {
				log.Error("SIGHUP: cannot read the configuration files; keeping the configuration in force", "err", err)
				continue
			}
			if len(files) == 0 {
				log.Warn("SIGHUP: no configuration files to import; the configuration is in the database (beacon config import DIR loads files)")
				continue
			}
			c, err := cdb.ImportOnSignal(ctx, files)
			switch {
			case err != nil:
				log.Error("SIGHUP: the configuration files were not imported; keeping the configuration in force", "err", err)
			case c.Unchanged:
				log.Info("SIGHUP: the configuration files hold the configuration in force", "version", c.Version)
			default:
				log.Info("SIGHUP: imported the configuration files", "version", c.Version, "items", len(c.Items), "now", cdb.Summary())
			}
		}
	}
}

// checkDatabase validates the configuration in the database: through the running service when it
// answers on its socket, else in the database file itself (read-only, so the service may be
// running).
func checkDatabase(cfg *config.Config) (string, error) {
	dbPath := storagePath(cfg)
	boltPath := filepath.Join(cfg.DataDir, "status.db")
	if cfg.Storage.Driver == "postgres" {
		dbPath = "postgres (" + cfg.Storage.DSNFile + ")"
	} else {
		dbThere, boltThere := fileThere(dbPath), fileThere(boltPath)
		switch {
		case !dbThere && !boltThere:
			return "none yet at " + dbPath + "; the service imports the files when it first starts", nil
		case !dbThere && boltThere:
			return fmt.Sprintf("a bbolt database at %s; the service migrates it into %s when it next starts", boltPath, dbPath), nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, _, err := ctl.Dial(ctx, ctl.Path(cfg.DataDir)); err == nil {
		s, err := c.CheckConfig(ctx)
		switch {
		case err != nil:
			return "", fmt.Errorf("database (the running service): %w", err)
		case s.Problem != "":
			return "", fmt.Errorf("database (the running service), version %d: %s", s.Version, s.Problem)
		}
		return fmt.Sprintf("version %d in the running service: %s", s.Version, s.Summary) + warningLines(s.Warnings), nil
	}
	st, err := store.OpenReadWith(storeOptions(cfg))
	if err != nil {
		return "", fmt.Errorf("database %s: %w (it may have been left mid-write: start the service once, or stop it cleanly, and check again)", dbPath, err)
	}
	defer func() { _ = st.Close() }()
	cdb := configdb.Open(configdb.Deps{Store: st})
	if err := cdb.Check(); err != nil {
		return "", fmt.Errorf("database %s, version %d: %w", dbPath, cdb.Version(), err)
	}
	if cdb.Version() == 0 {
		return "no configuration yet; the service imports the files when it next starts", nil
	}
	return fmt.Sprintf("version %d: %s", cdb.Version(), cdb.Summary()) + warningLines(cdb.Warnings()), nil
}

// fileThere reports whether a file exists.
func fileThere(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// warningLines are the warnings of a check or a change, one per line: what the configuration names that
// Beacon does not know, which stops nothing.
func warningLines(ws []string) string {
	var sb strings.Builder
	for _, w := range ws {
		sb.WriteString("\n  warning: " + w)
	}
	return sb.String()
}

const configUsage = "config import DIR [-dry-run] [-note TEXT] | config export DIR | config history [-limit 20] | config revert VERSION [-note TEXT]"

// configCmd imports, exports, lists and reverts the configuration of the running service.
func configCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, configUsage)
		return 2
	}
	fs := flag.NewFlagSet("config "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "say what the import would change, and change nothing")
	note := fs.String("note", "", "why: kept with the change and shown in the alert chat")
	limit := fs.Int("limit", 20, "how many changes to list")
	pos, ok := positional(fs, args[1:])
	if !ok {
		return 2
	}
	ctx := context.Background()
	switch {
	case args[0] == "import" && len(pos) == 1:
		files, err := configdb.ReadDir(pos[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		ch, err := c.ImportConfig(ctx, ctl.ImportRequest{Files: files, Note: *note, DryRun: *dryRun})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printChange(stdout, ch, *dryRun)
		return 0
	case args[0] == "export" && len(pos) == 1:
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		out, err := c.ExportConfig(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		written, removed, err := configdb.WriteDir(pos[0], out.Files)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "exported version %d to %s: %d files\n", out.Version, pos[0], len(written))
		for _, p := range removed {
			fmt.Fprintf(stdout, "  removed %s, which the configuration does not hold\n", p)
		}
		return 0
	case args[0] == "history" && len(pos) == 0:
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		list, err := c.ConfigHistory(ctx, *limit)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "VERSION\tTIME\tWHO\tVIA\tACTION\tITEMS\tNOTE")
		for _, ch := range list {
			what := strconv.Itoa(len(ch.Items))
			if len(ch.Items) == 1 {
				what = ch.Items[0].Op + " " + ch.Items[0].Kind + " " + ch.Items[0].Key
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", ch.Version, ch.Time.Local().Format(time.DateTime), ch.Actor, ch.Via, ch.Action, what, ch.Note)
		}
		_ = tw.Flush()
		return 0
	case args[0] == "revert" && len(pos) == 1:
		v, err := strconv.ParseInt(pos[0], 10, 64)
		if err != nil || v < 1 {
			fmt.Fprintln(stderr, "config revert VERSION: the version is a whole number, as config history lists it")
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		ch, err := c.RevertConfig(ctx, ctl.RevertRequest{Version: v, Note: *note})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		printChange(stdout, ch, false)
		return 0
	}
	fmt.Fprintln(stderr, configUsage)
	return 2
}

func printChange(w io.Writer, ch store.ConfigChange, dryRun bool) {
	switch {
	case ch.Unchanged:
		fmt.Fprintf(w, "nothing to change: version %d holds this already%s\n", ch.Version, warningLines(ch.Warnings))
		return
	case dryRun:
		fmt.Fprintf(w, "would change %d items (nothing changed):\n", len(ch.Items))
	default:
		fmt.Fprintf(w, "version %d: %s of %d items\n", ch.Version, ch.Action, len(ch.Items))
	}
	if len(ch.Items) == 0 {
		fmt.Fprintln(w, "  only the module file's comments or layout")
	}
	for _, it := range ch.Items {
		fmt.Fprintf(w, "  %s %s %s\n", map[string]string{"added": "+", "changed": "~", "removed": "-"}[it.Op], it.Kind, it.Key)
	}
	if len(ch.Warnings) > 0 {
		fmt.Fprintln(w, strings.TrimPrefix(warningLines(ch.Warnings), "\n"))
	}
}
