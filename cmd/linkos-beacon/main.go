// Command linkos-beacon is Beacon's service: monitoring with alert delivery on Feishu and phones, the
// SSH channel through which agents reach machines, a REST API and an MCP server.
//
//	linkos-beacon serve [-config path]          run the service
//	linkos-beacon check-config [-config path]   validate the configuration, its files and the database
//	linkos-beacon config import DIR [-dry-run] | config export DIR | config history | config revert VERSION
//	linkos-beacon token create NAME -scopes a,b [-expires 365d] [-ssh-key KEY|@FILE] [-machines a,b]
//	linkos-beacon token list | token revoke NAME
//	linkos-beacon token ssh-key NAME add KEY|@FILE | remove FINGERPRINT; token machines NAME a,b|all
//	linkos-beacon machine add NAME HOST[:PORT] | machine list | machine remove NAME | machine ca
//	linkos-beacon ssh pause | ssh resume | ssh status
//	linkos-beacon enroll probe|host|heartbeat NAME [-rotate]
//	linkos-beacon drill fail 15m | drill off | drill status
//	linkos-beacon healthcheck [-url http://127.0.0.1:17001/healthz]   health check for the container
//	linkos-beacon version
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
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/clientip"
	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/configdb"
	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/gateway"
	"github.com/openlinkos/beacon/internal/inventory"
	"github.com/openlinkos/beacon/internal/mcpserver"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/notify"
	"github.com/openlinkos/beacon/internal/router"
	"github.com/openlinkos/beacon/internal/server"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/version"
	"github.com/openlinkos/beacon/internal/vm"
)

const defaultConfig = "/etc/linkos-beacon/config.yaml"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  linkos-beacon serve [-config path]          run the service
  linkos-beacon check-config [-config path]   validate the configuration, its files and the database
  linkos-beacon config import DIR [-dry-run] [-note TEXT]
                                              make DIR/catalog, DIR/targets and DIR/targets/modules.yml
                                              the catalogue, probe targets and modules, as one change
  linkos-beacon config export DIR             write them there in the same layout
  linkos-beacon config history [-limit 20] | config revert VERSION [-note TEXT]
  linkos-beacon token create NAME -scopes status:read,alerts:write [-expires 365d]
                [-ssh-key KEY|@FILE] [-machines a,b]   keys for the SSH channel; the machines it reaches
  linkos-beacon token list
  linkos-beacon token revoke NAME
  linkos-beacon token ssh-key NAME add KEY|@FILE | token ssh-key NAME remove FINGERPRINT
  linkos-beacon token machines NAME a,b|all
  linkos-beacon machine add NAME HOST[:PORT] [-user beacon] [-critical] [-note TEXT] [-replace]
                                              pin a machine's host key; prints what the machine needs
  linkos-beacon machine list | machine remove NAME | machine ca
  linkos-beacon ssh pause | ssh resume | ssh status
                                              stop or allow agents' access to machines
  linkos-beacon enroll probe|host|heartbeat NAME [-rotate]
                                              a machine's gateway token (printed once, alone, on stdout)
  linkos-beacon drill fail 15m | drill off | drill status
  linkos-beacon healthcheck [-url URL]        exit 0 when the service answers (the image's health check)
  linkos-beacon version
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
	case "config":
		return configCmd(args[1:], stdout, stderr)
	case "token":
		return token(args[1:], stdout, stderr)
	case "drill":
		return drill(args[1:], stdout, stderr)
	case "enroll":
		return enroll(args[1:], stdout, stderr)
	case "machine":
		return machine(args[1:], stdout, stderr)
	case "ssh":
		return sshCmd(args[1:], stdout, stderr)
	case "healthcheck":
		return healthcheck(args[1:], stderr)
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

// checkConfig validates the configuration file, the catalogue, targets and module files it names
// (which are imported into the database) and the configuration in the database.
func checkConfig(args []string, stdout, stderr io.Writer) int {
	path, _, ok := configFlag("check-config", args, stderr)
	if !ok {
		return 2
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sshListen := "off"
	if cfg.SSH.CAKeyFile != "" {
		if _, _, err := sshKeys(cfg); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		sshListen = cfg.SSH.Listen
	}
	files, err := configFiles(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	inFiles := "none"
	if len(files) > 0 {
		st, err := configdb.ParseFiles(files)
		if err != nil {
			fmt.Fprintf(stderr, "configuration files (catalog_dir %s, gateway.targets_dir %s, gateway.modules_file %s): %v\n",
				cfg.CatalogDir, cfg.Gateway.TargetsDir, cfg.Gateway.ModulesFile, err)
			return 1
		}
		inFiles = st.Summary()
	}
	inDB, err := checkDatabase(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "configuration OK: Feishu app %v, Pushover %v, Bark %v; gateway %q; SSH channel %s\n",
		cfg.Feishu.AppIDFile != "", cfg.Pushover.TokenFile != "", cfg.Bark.URLFile != "", cfg.Gateway.Listen, sshListen)
	fmt.Fprintf(stdout, "files: %s\n", inFiles)
	fmt.Fprintf(stdout, "database: %s\n", inDB)
	return 0
}

func serve(args []string, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	path, _, ok := configFlag("serve", args, stderr)
	if !ok {
		return 2
	}
	cfg, err := config.Load(path)
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
	sec := cfg.Secrets
	var bot *notify.GroupBot // the chat's custom bot: plain messages and echoes
	if sec.FeishuWebhookURL != "" {
		bot = &notify.GroupBot{URL: sec.FeishuWebhookURL, Secret: sec.FeishuWebhookSecret, Keyword: cfg.Feishu.WebhookKeyword}
	}
	// the catalogue, probe targets and modules, which every change through Beacon updates at once
	cdb, err := openConfig(context.Background(), cfg, st, bot, log)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	m := metrics.New(version.Version)
	catalog.Register(m, cdb.Catalog)
	m.Register("linkos_token_expiry_timestamp_seconds", "When an API or gateway token expires", "gauge", func() []metrics.Sample {
		var out []metrics.Sample
		for _, t := range st.Tokens() {
			out = append(out, metrics.Sample{Labels: metrics.Labels("token", t.Name, "subject", t.Subject), Value: float64(t.Expires.Unix())})
		}
		return out
	})

	var br *bridge.Bridge
	rcfg, err := routerConfig(cfg.Routing)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	rt := router.New(rcfg, st, sinkFunc{hook: func(ctx context.Context, w alertmanager.Webhook) error { return br.HandleWebhook(ctx, w) },
		watchdog: func() { br.Watchdog() }}, nil, log)
	m.Register("linkos_beacon_alerts_firing", "Firing alerts the router holds", "gauge", func() []metrics.Sample {
		return []metrics.Sample{{Value: float64(rt.Firing())}}
	})
	deps := bridge.Deps{
		Config: cfg, Store: st, AM: rt,
		Catalog: cdb.Catalog, Metrics: m, Log: log,
	}
	if cfg.VictoriaMetrics.URL != "" {
		deps.VM = vm.New(cfg.VictoriaMetrics.URL, nil)
	}
	if sec.FeishuAppID != "" {
		deps.Feishu = feishu.New(sec.FeishuAppID, sec.FeishuAppSecret, cfg.Feishu.Domain, log, func() { br.CountFeishuCall() })
	}
	if bot != nil {
		deps.Bot = bot
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
	gate, err := newGate(cfg, st, deps.Bot, m, log)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}

	auth := &api.Auth{Store: st, IP: clientip.New(cfg.Proxies, false)}
	sd := server.Deps{
		Bridge: br, API: &api.Server{Bridge: br, SSH: gate, SSHAddress: cfg.SSH.PublicAddress, Auth: auth, Log: log, Config: cdb},
		MCP:     mcpserver.Handler(br, mcpserver.SSH{Gate: gate, Address: cfg.SSH.PublicAddress}, auth, log, mcpserver.WithConfig(cdb)),
		Metrics: m, Drill: server.DrillFrom(st.Time), Log: log,
	}
	if deps.Feishu != nil && cfg.Feishu.Callback == "http" {
		sd.Feishu = feishu.HTTPHandler(sec.FeishuVerificationToken, sec.FeishuEncryptKey, br.HandleAction)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reloadOnHUP(ctx, cfg, cdb, log)
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
	rt.Restore(st.Groups())
	go rt.Run(ctx)
	inv := &inventory.Checker{Catalog: cdb.Catalog}
	inv.Register(m)
	go inv.Run(ctx)

	active := func(id string) error {
		c := cdb.Catalog()
		l, ok := c.Location(id)
		switch {
		case !ok:
			return fmt.Errorf("location %s is not in the catalogue", id)
		case !c.Expected(l, time.Now()):
			return fmt.Errorf("the machine of location %s has expired", id)
		}
		return nil
	}
	if ln, err := ctl.Listen(ctl.Path(cfg.DataDir)); err == nil {
		var machines ctl.Machines // an interface holding a nil *Gate would not be nil
		if gate != nil {
			machines = gate
		}
		socket := http.NewServeMux()
		socket.Handle("/v1/config/", ctl.ConfigHandler(cdb))
		socket.Handle("/", ctl.Handler(st, active, machines))
		cs := &http.Server{Handler: socket, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = cs.Serve(ln) }()
		defer func() { _ = cs.Close() }()
	} else if !errors.Is(err, errors.ErrUnsupported) {
		log.Warn("admin socket unavailable; token and drill commands will not work", "err", err)
	}

	hs := &http.Server{Addr: cfg.Listen, Handler: server.Handler(sd), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 3)
	go func() { errc <- hs.ListenAndServe() }()
	var gs *http.Server
	if cfg.Gateway.Listen != "" {
		vmURL, _ := url.Parse(cfg.VictoriaMetrics.URL)
		gs = &http.Server{Addr: cfg.Gateway.Listen, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute,
			WriteTimeout: 2 * time.Minute, IdleTimeout: 5 * time.Minute,
			Handler: gateway.Handler(gateway.Deps{Auth: auth, Catalog: cdb.Catalog, Targets: cdb.Targets,
				Modules: cdb.Modules, VM: vmURL, Alerts: rt.Ingest, RulesToken: sec.RulesToken,
				Metrics: m, Log: log})}
		go func() { errc <- gs.ListenAndServe() }()
	}
	if gate != nil {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.SSH.Listen)
		if err != nil {
			log.Error("cannot start", "err", fmt.Errorf("ssh.listen: %w", err))
			return 1
		}
		defer func() { _ = ln.Close() }()
		go func() {
			if err := gate.Serve(ln); err != nil {
				errc <- err
			}
		}()
		go gate.Tidy(ctx)
	}
	log.Info("linkos-beacon started", "version", version.Version, "listen", cfg.Listen,
		"feishu_app", deps.Feishu != nil, "callback", cfg.Feishu.Callback, "pushover", deps.Pushover != nil, "locations", len(cdb.Catalog().Locations),
		"gateway", cfg.Gateway.Listen, "targets", cdb.Targets().Count(), "config_version", cdb.Version(),
		"ssh", map[bool]string{true: cfg.SSH.Listen, false: "off"}[gate != nil])
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
	if gs != nil {
		_ = gs.Shutdown(sctx)
	}
	log.Info("linkos-beacon stopped")
	return 0
}

// sinkFunc hands the router's groups to the bridge, which is created after the router.
type sinkFunc struct {
	hook     func(context.Context, alertmanager.Webhook) error
	watchdog func()
}

func (s sinkFunc) HandleWebhook(ctx context.Context, w alertmanager.Webhook) error {
	return s.hook(ctx, w)
}
func (s sinkFunc) Watchdog() { s.watchdog() }

// routerConfig compiles the routing section.
func routerConfig(c config.Routing) (router.Config, error) {
	out := router.Config{GroupBy: c.GroupBy, GroupWait: c.GroupWait.D(), GroupInterval: c.GroupInterval.D(), ResolveTimeout: c.ResolveTimeout.D()}
	for _, r := range c.Routes {
		m, err := router.CompileLabels(r.Match)
		if err != nil {
			return out, err
		}
		out.Routes = append(out.Routes, router.Route{Match: m, GroupBy: r.GroupBy})
	}
	for _, in := range c.Inhibit {
		src, err := router.CompileLabels(in.Source)
		if err != nil {
			return out, err
		}
		dst, err := router.CompileLabels(in.Target)
		if err != nil {
			return out, err
		}
		out.Inhibit = append(out.Inhibit, router.Inhibit{Source: src, Target: dst, Equal: in.Equal})
	}
	return out, nil
}

// enroll gives a machine its gateway token. The secret alone goes to stdout, so that a deploy script can
// pass it straight to the machine; everything else goes to stderr.
func enroll(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rotate := fs.Bool("rotate", false, "replace the token of a machine that is enrolled already")
	var pos []string
	rest := args
	for len(rest) > 0 { // flags may come before or after the two arguments
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		rest = fs.Args()
		if len(rest) > 0 {
			pos, rest = append(pos, rest[0]), rest[1:]
		}
	}
	if len(pos) != 2 || ctl.EnrollKinds[pos[0]] == "" {
		fmt.Fprintln(stderr, "enroll probe|host|heartbeat NAME [-rotate]")
		return 2
	}
	c, ok := dial(stderr)
	if !ok {
		return 1
	}
	out, err := c.Enroll(context.Background(), ctl.EnrollRequest{Kind: pos[0], Subject: pos[1], Rotate: *rotate})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, out.Secret)
	fmt.Fprintf(stderr, "enrolled %s (%s, expires %s); the token above is not shown again\n",
		out.Token.Name, strings.Join(out.Token.Scopes, ","), out.Token.Expires.Local().Format(time.DateOnly))
	return 0
}

func dial(stderr io.Writer) (*ctl.Client, bool) {
	dataDir := os.Getenv("LINKOS_BEACON_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/linkos-beacon"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := ctl.Dial(ctx, ctl.Path(dataDir))
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			fmt.Fprintf(stderr, "cannot reach the running service at %s: %v\n(run this inside the container: docker exec linkos-beacon linkos-beacon …)\n", ctl.Path(dataDir), err)
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
		sshKey := fs.String("ssh-key", "", "a public key for the SSH channel (an authorized_keys line), or @FILE")
		machines := fs.String("machines", "", "comma-separated machines the token reaches through the SSH channel; all when empty")
		if err := fs.Parse(args[2:]); err != nil {
			return 2
		}
		req := ctl.TokenRequest{Name: args[1], Scopes: strings.Split(*scopes, ","), TTL: *expires}
		if *sshKey != "" {
			k, err := keyArg(*sshKey)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			req.SSHKeys = []string{k}
		}
		if *machines != "" {
			req.Machines = strings.Split(*machines, ",")
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		out, err := c.CreateToken(ctx, req)
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
	case "ssh-key", "machines":
		return tokenSSH(args, stdout, stderr)
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
func healthcheck(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
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
