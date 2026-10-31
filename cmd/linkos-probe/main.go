// Command linkos-probe is the probe agent: it checks targets for the status service (like the
// Blackbox Exporter, with the same metric names, plus a failure reason and multi-step checks) and
// reports a few host metrics (like a small Node Exporter). vmagent scrapes it and writes the results to
// the status service's gateway.
//
//	linkos-probe serve [-listen 127.0.0.1:17009] [-modules FILE] [-textfile DIR] [-proc /proc]
//	linkos-probe check [-modules FILE]                 validate the module file
//	linkos-probe run [-modules FILE] MODULE TARGET     probe once and print the result
//	linkos-probe version
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
	"syscall"
	"time"

	"github.com/openlinkos/status/internal/hostmetrics"
	"github.com/openlinkos/status/internal/probe"
	"github.com/openlinkos/status/internal/version"
)

const defaultModules = "/etc/linkos-agent/modules.yml"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  linkos-probe serve [-listen 127.0.0.1:17009] [-modules FILE] [-textfile DIR] [-proc /proc]
  linkos-probe check [-modules FILE]                 validate the module file
  linkos-probe run [-modules FILE] MODULE TARGET     probe once and print the result
  linkos-probe version
`)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stderr)
	case "check":
		return check(args[1:], stdout, stderr)
	case "run":
		return once(args[1:], stdout, stderr)
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, version.String())
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n", args[0])
	usage(stderr)
	return 2
}

func userAgent() string { return "linkos-probe/" + version.Version }

func serve(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:17009", "address to serve /probe and /metrics on")
	modules := fs.String("modules", defaultModules, "module file")
	textfile := fs.String("textfile", "/var/lib/linkos-agent/textfile", "directory of *.prom files (heartbeats); empty for none")
	proc := fs.String("proc", "/proc", "proc filesystem")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	ms, err := probe.LoadModules(*modules)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	var mods probe.Reloadable
	mods.Store(ms)
	p := &probe.Prober{UserAgent: userAgent()}
	host := &hostmetrics.Collector{ProcRoot: *proc, TextfileDir: *textfile}

	mux := http.NewServeMux()
	mux.Handle("GET /probe", probe.Handler(p, mods.Load, func(module, target string, r probe.Result) {
		log.Info("probe failed", "module", module, "target", target, "reason", r.Reason, "detail", r.Detail)
	}))
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		samples := host.Collect()
		samples = append(samples, probe.Sample{Name: "linkos_probe_build_info", Help: "The probe agent's version", Labels: `version="` + version.Version + `"`, Value: 1},
			probe.Sample{Name: "linkos_probe_modules", Help: "Modules loaded from the module file", Value: float64(len(mods.Load().Modules))})
		if rss, ok := host.SelfRSS(); ok {
			samples = append(samples, probe.Sample{Name: "process_resident_memory_bytes", Help: "Resident memory size in bytes", Value: rss})
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = probe.WriteSamples(w, samples)
		for _, l := range host.Textfile() {
			_, _ = io.WriteString(w, l+"\n")
		}
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if ms, err := probe.LoadModules(*modules); err != nil {
					log.Error("module reload failed; keeping the previous modules", "err", err)
				} else {
					mods.Store(ms)
					log.Info("modules reloaded", "modules", len(ms.Modules))
				}
			}
		}
	}()
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("linkos-probe started", "version", version.Version, "listen", *listen, "modules", len(ms.Modules))
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			return 1
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	return 0
}

func check(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	modules := fs.String("modules", defaultModules, "module file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ms, err := probe.LoadModules(*modules)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "modules OK: %d\n", len(ms.Modules))
	return 0
}

func once(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	modules := fs.String("modules", defaultModules, "module file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "run [-modules FILE] MODULE TARGET")
		return 2
	}
	ms, err := probe.LoadModules(*modules)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	m := ms.Modules[fs.Arg(0)]
	if m == nil {
		fmt.Fprintf(stderr, "no module %q\n", fs.Arg(0))
		return 2
	}
	r := (&probe.Prober{UserAgent: userAgent()}).Run(context.Background(), m, fs.Arg(1))
	_ = probe.Write(stdout, r)
	if !r.Success {
		fmt.Fprintf(stderr, "failed: %s: %s\n", r.Reason, r.Detail)
		return 1
	}
	return 0
}
