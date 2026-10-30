// Command linkos-status is the LinkOS Status service: status pages, alert delivery with a closed loop
// on Feishu and phones, a REST API and an MCP server.
//
//	linkos-status serve [-config path]          run the service
//	linkos-status check-config [-config path]   validate the configuration and the catalogue
//	linkos-status token create NAME -scopes a,b [-expires 365d]
//	linkos-status token list | token revoke NAME
//	linkos-status drill fail 15m | drill off | drill status
//	linkos-status probe [-url http://127.0.0.1:17001/healthz]   health check for the container
//	linkos-status version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/openlinkos/status/internal/alertmanager"
	"github.com/openlinkos/status/internal/api"
	"github.com/openlinkos/status/internal/bridge"
	"github.com/openlinkos/status/internal/catalog"
	"github.com/openlinkos/status/internal/clientip"
	"github.com/openlinkos/status/internal/config"
	"github.com/openlinkos/status/internal/ctl"
	"github.com/openlinkos/status/internal/feishu"
	"github.com/openlinkos/status/internal/mcpserver"
	"github.com/openlinkos/status/internal/metrics"
	"github.com/openlinkos/status/internal/notify"
	"github.com/openlinkos/status/internal/server"
	"github.com/openlinkos/status/internal/store"
	"github.com/openlinkos/status/internal/version"
	"github.com/openlinkos/status/internal/vm"
)

const defaultConfig = "/etc/linkos-status/config.yaml"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  linkos-status serve [-config path]          run the service
  linkos-status check-config [-config path]   validate the configuration and the catalogue
  linkos-status token create NAME -scopes status:read,alerts:write [-expires 365d]
  linkos-status token list
  linkos-status token revoke NAME
  linkos-status drill fail 15m | drill off | drill status
  linkos-status probe [-url URL]              exit 0 when the service answers (the image's health check)
  linkos-status version
Scopes: `+strings.Join(store.Scopes, ", ")+`
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
	case "check-config":
		return checkConfig(args[1:], stdout, stderr)
	case "token":
		return token(args[1:], stdout, stderr)
	case "drill":
		return drill(args[1:], stdout, stderr)
	case "probe":
		return probe(args[1:], stderr)
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

func configFlag(name string, args []string, stderr io.Writer) (string, []string, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", defaultConfig, "configuration file")
	if err := fs.Parse(args); err != nil {
		return "", nil, false
	}
	return *path, fs.Args(), true
}

func load(path string) (*config.Config, *catalog.Catalog, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	cat, err := catalog.Load(cfg.CatalogDir)
	if err != nil {
		return nil, nil, fmt.Errorf("catalogue %s: %w", cfg.CatalogDir, err)
	}
	return cfg, cat, nil
}

func checkConfig(args []string, stdout, stderr io.Writer) int {
	path, _, ok := configFlag("check-config", args, stderr)
	if !ok {
		return 2
	}
	cfg, cat, err := load(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "configuration OK: %d probe locations, %d heartbeats, %d resources; Feishu app %v, Pushover %v, Bark %v\n",
		len(cat.Locations), len(cat.Heartbeats), len(cat.Resources),
		cfg.Feishu.AppIDFile != "", cfg.Pushover.TokenFile != "", cfg.Bark.URLFile != "")
	return 0
}

func serve(args []string, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	path, _, ok := configFlag("serve", args, stderr)
	if !ok {
		return 2
	}
	cfg, cat, err := load(path)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "status.db"))
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	var catalogue atomic.Pointer[catalog.Catalog]
	catalogue.Store(cat)
	m := metrics.New(version.Version)
	catalog.Register(m, catalogue.Load)

	var br *bridge.Bridge
	deps := bridge.Deps{
		Config: cfg, Store: st, AM: alertmanager.New(cfg.Alertmanager.URL, nil),
		Catalog: catalogue.Load, Metrics: m, Log: log,
	}
	if cfg.VictoriaMetrics.URL != "" {
		deps.VM = vm.New(cfg.VictoriaMetrics.URL, nil)
	}
	sec := cfg.Secrets
	if sec.FeishuAppID != "" {
		deps.Feishu = feishu.New(sec.FeishuAppID, sec.FeishuAppSecret, cfg.Feishu.Domain, log, func() { br.CountFeishuCall() })
	}
	if sec.FeishuWebhookURL != "" {
		deps.Bot = &notify.GroupBot{URL: sec.FeishuWebhookURL, Secret: sec.FeishuWebhookSecret, Keyword: cfg.Feishu.WebhookKeyword}
	}
	if sec.PushoverToken != "" {
		deps.Pushover = &notify.Pushover{API: cfg.Pushover.API, Token: sec.PushoverToken, User: sec.PushoverUserKey,
			Retry: cfg.Pushover.Retry.D(), Expire: cfg.Pushover.Expire.D()}
	}
	if sec.BarkURL != "" {
		deps.Bark = &notify.Bark{URL: sec.BarkURL}
	}
	var listener *feishu.Listener
	if deps.Feishu != nil && cfg.Feishu.Callback == "websocket" {
		listener = feishu.Listen(sec.FeishuAppID, sec.FeishuAppSecret, cfg.Feishu.Domain, func(ctx context.Context, a feishu.Action) feishu.Response {
			return br.HandleAction(ctx, a)
		}, log)
		deps.WSReady = listener.Ready
	}
	br = bridge.New(deps)

	auth := &api.Auth{Store: st, IP: clientip.New(cfg.Proxies, false)}
	sd := server.Deps{
		Bridge: br, API: &api.Server{Bridge: br, Auth: auth, Log: log}, MCP: mcpserver.Handler(br, auth, log),
		Metrics: m, WebhookToken: sec.AlertmanagerWebhookToken, Drill: server.DrillFrom(st.Time), Log: log,
	}
	if deps.Feishu != nil && cfg.Feishu.Callback == "http" {
		sd.Feishu = feishu.HTTPHandler(sec.FeishuVerificationToken, sec.FeishuEncryptKey, br.HandleAction)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reloadOnHUP(ctx, cfg.CatalogDir, &catalogue, log)
	if listener != nil {
		go func() {
			if err := listener.Run(ctx); err != nil {
				log.Error("feishu long connection stopped", "err", err)
			}
		}()
	}
	var ping func(context.Context, bool) error
	if sec.HealthchecksBridgeURL != "" {
		ping = func(ctx context.Context, ok bool) error { return notify.Ping(ctx, nil, sec.HealthchecksBridgeURL, ok) }
	}
	go br.Run(ctx, ping)

	if ln, err := ctl.Listen(ctl.Path(cfg.DataDir)); err == nil {
		cs := &http.Server{Handler: ctl.Handler(st), ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = cs.Serve(ln) }()
		defer func() { _ = cs.Close() }()
	} else if !errors.Is(err, errors.ErrUnsupported) {
		log.Warn("admin socket unavailable; token and drill commands will not work", "err", err)
	}

	hs := &http.Server{Addr: cfg.Listen, Handler: server.Handler(sd), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("linkos-status started", "version", version.Version, "listen", cfg.Listen,
		"feishu_app", deps.Feishu != nil, "callback", cfg.Feishu.Callback, "pushover", deps.Pushover != nil, "locations", len(cat.Locations))
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			return 1
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	log.Info("linkos-status stopped")
	return 0
}

// reloadOnHUP reloads the catalogue when the process gets SIGHUP; a broken catalogue is logged and
// the previous one kept.
func reloadOnHUP(ctx context.Context, dir string, cat *atomic.Pointer[catalog.Catalog], log *slog.Logger) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	defer signal.Stop(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			c, err := catalog.Load(dir)
			if err != nil {
				log.Error("catalogue reload failed; keeping the previous one", "err", err)
				continue
			}
			cat.Store(c)
			log.Info("catalogue reloaded", "locations", len(c.Locations), "heartbeats", len(c.Heartbeats), "resources", len(c.Resources))
		}
	}
}

func dial(stderr io.Writer) (*ctl.Client, bool) {
	dataDir := os.Getenv("LINKOS_STATUS_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/linkos-status"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := ctl.Dial(ctx, ctl.Path(dataDir))
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			fmt.Fprintf(stderr, "cannot reach the running service at %s: %v\n(run this inside the container: docker exec linkos-status linkos-status …)\n", ctl.Path(dataDir), err)
		} else {
			fmt.Fprintln(stderr, err)
		}
		return nil, false
	}
	return c, true
}

func token(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "create":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			fmt.Fprintln(stderr, "token create NAME -scopes a,b [-expires 365d]")
			return 2
		}
		fs := flag.NewFlagSet("token create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		scopes := fs.String("scopes", "status:read", "comma-separated scopes: "+strings.Join(store.Scopes, ", "))
		expires := fs.String("expires", "365d", "lifetime, such as 90d or 720h")
		if err := fs.Parse(args[2:]); err != nil {
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		out, err := c.CreateToken(ctx, ctl.TokenRequest{Name: args[1], Scopes: strings.Split(*scopes, ","), TTL: *expires})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stderr, "token %s created with %s, expires %s. The secret is shown only now:\n", out.Token.Name, strings.Join(out.Token.Scopes, ","), out.Token.Expires.Format(time.DateOnly))
		fmt.Fprintln(stdout, out.Secret)
		return 0
	case "list":
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		list, err := c.Tokens(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSCOPES\tEXPIRES\tCREATED BY")
		for _, t := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, strings.Join(t.Scopes, ","), t.Expires.Format(time.DateOnly), t.CreatedBy)
		}
		_ = tw.Flush()
		return 0
	case "revoke":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "token revoke NAME")
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		if err := c.RevokeToken(ctx, args[1]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "token %s revoked\n", args[1])
		return 0
	}
	usage(stderr)
	return 2
}

func drill(args []string, stdout, stderr io.Writer) int {
	ctx := context.Background()
	var verb, arg string
	for i, a := range args {
		switch i {
		case 0:
			verb = a
		case 1:
			arg = a
		default:
			verb = "" // too many arguments
		}
	}
	if (verb != "fail" || arg == "") && ((verb != "off" && verb != "status") || arg != "") {
		fmt.Fprintln(stderr, "drill fail 15m | drill off | drill status")
		return 2
	}
	c, ok := dial(stderr)
	if !ok {
		return 1
	}
	var (
		state ctl.DrillState
		err   error
	)
	switch verb {
	case "fail":
		state, err = c.Drill(ctx, arg)
	case "off":
		state, err = c.Drill(ctx, "off")
	default:
		state, err = c.DrillStatus(ctx)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if state.Until.IsZero() || time.Now().After(state.Until) {
		fmt.Fprintln(stdout, "no drill: /drill/healthz answers 200")
	} else {
		fmt.Fprintf(stdout, "drill until %s: /drill/healthz answers 503\n", state.Until.Local().Format(time.DateTime))
	}
	return 0
}

// probe asks the local service for /healthz; the image has no shell or curl for its health check.
func probe(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "http://127.0.0.1:17001/healthz", "address to check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, resp.Status)
		return 1
	}
	return 0
}
