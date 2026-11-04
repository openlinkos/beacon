// Command beacon-oncall is the reference harness that runs an agent when Beacon wakes it. It takes
// Beacon's signed webhook, answers at once, and starts the program named with -run (through /bin/sh -c)
// with the payload on its standard input and the alert in BEACON_* variables. Each run's output goes to
// a file under -log-dir. The harness decides nothing: the program does. See docs/guide/on-call.md, "Running an
// agent when woken", and contrib/oncall for wrappers around Claude Code and Codex.
//
//	beacon-oncall -secret-file FILE -run CMD [-run-resolved CMD] [-listen :17007] [-concurrency 2]
//	              [-timeout 30m] [-log-dir /var/lib/beacon-oncall/logs] [-keep 720h]
//	beacon-oncall version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/openlinkos/beacon/internal/oncall"
	"github.com/openlinkos/beacon/internal/version"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  beacon-oncall -secret-file FILE -run CMD [-run-resolved CMD] [-listen :17007] [-concurrency 2]
                [-timeout 30m] [-log-dir /var/lib/beacon-oncall/logs] [-keep 720h]
                                                     take Beacon's webhook and run the program for each alert
  beacon-oncall version
`)
}

func versionString() string {
	s := "beacon-oncall " + version.Version
	if version.Commit != "" {
		s += " (" + version.Commit
		if version.Date != "" {
			s += ", " + version.Date
		}
		s += ")"
	}
	return fmt.Sprintf("%s %s %s/%s", s, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "-version", "--version":
			fmt.Fprintln(stdout, versionString())
			return 0
		case "help", "-h", "--help":
			usage(stdout)
			return 0
		}
	}
	fs := flag.NewFlagSet("beacon-oncall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr); fs.PrintDefaults() }
	listen := fs.String("listen", ":17007", "address to take Beacon's webhook on")
	secretFile := fs.String("secret-file", "", "file with the hook's secret, the same as Beacon's secret_file (required)")
	runCmd := fs.String("run", "", "program for firing events, run through /bin/sh -c (required)")
	runResolved := fs.String("run-resolved", "", "program for resolved events; without it they are acknowledged and nothing runs")
	concurrency := fs.Int("concurrency", 2, "programs running at once; the rest wait, at most 32")
	timeout := fs.Duration("timeout", 30*time.Minute, "time a program gets before its process group is killed")
	logDir := fs.String("log-dir", "/var/lib/beacon-oncall/logs", "directory for each run's output, as <time>-<delivery>.log")
	keep := fs.Duration("keep", 720*time.Hour, "logs older than this are removed; 0 keeps them all")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		usage(stderr)
		return 2
	}
	if *secretFile == "" || *runCmd == "" {
		fmt.Fprintln(stderr, "-secret-file and -run are required")
		usage(stderr)
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	secret, err := os.ReadFile(*secretFile)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	secret = []byte(strings.TrimSpace(string(secret)))
	if len(secret) == 0 {
		log.Error("cannot start: the secret file is empty")
		return 1
	}
	if len(secret) < 24 {
		log.Warn("the secret is short; Beacon asks for at least 24 characters")
	}
	s, err := oncall.New(oncall.Options{Secret: secret, Run: *runCmd, RunResolved: *runResolved, Concurrency: *concurrency,
		Timeout: *timeout, LogDir: *logDir, Keep: *keep, Log: log})
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: *listen, Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("beacon-oncall started", "version", version.Version, "listen", *listen, "concurrency", *concurrency,
		"timeout", *timeout, "log_dir", *logDir, "resolved", *runResolved != "")
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			return 1
		}
	}
	// Stop taking deliveries, then let the programs finish: they get as long as one run may take.
	hctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Shutdown(hctx)
	cancel()
	log.Info("shutting down; waiting for the programs that run", "up_to", *timeout)
	wctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := s.Shutdown(wctx); err != nil {
		log.Warn("programs still running were killed", "err", err)
	}
	return 0
}
