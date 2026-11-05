// Command beacon is Beacon's service: monitoring with alert delivery on Feishu and phones, the
// SSH channel through which agents reach machines, a REST API and an MCP server.
//
//	beacon serve [-config path]                 run the service
//	beacon check-config [-config path]          validate the configuration, its files and the database
//	beacon config import DIR [-dry-run] | config export DIR | config history | config revert VERSION
//	beacon setup | user list|invite|disable|enable|role|reset | agent add|list|remove
//	beacon token create NAME -scopes a,b [-expires 365d] [-ssh-key KEY|@FILE] [-machines a,b]
//	beacon token list | token revoke NAME
//	beacon token ssh-key NAME add KEY|@FILE | remove FINGERPRINT; token machines NAME a,b|all
//	beacon machine add NAME HOST[:PORT]|connector | machine connector NAME -key KEY | machine list | machine remove NAME | machine ca
//	beacon ssh pause | ssh resume | ssh status
//	beacon silence create -matchers 'alertname=X,service=Y' -duration 4h -comment TEXT
//	beacon silence list | silence expire ID
//	beacon audit verify | storage check
//	beacon enroll probe|host|heartbeat NAME [-rotate]
//	beacon drill fail 15m | drill off | drill status
//	beacon healthcheck [-url http://127.0.0.1:17001/livez]     health check for the container
//	beacon version
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
	"github.com/openlinkos/beacon/internal/ha"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/inventory"
	"github.com/openlinkos/beacon/internal/mcpserver"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/notify"
	"github.com/openlinkos/beacon/internal/oauth"
	"github.com/openlinkos/beacon/internal/probenow"
	"github.com/openlinkos/beacon/internal/router"
	"github.com/openlinkos/beacon/internal/server"
	"github.com/openlinkos/beacon/internal/status"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/version"
	"github.com/openlinkos/beacon/internal/vm"
	"github.com/openlinkos/beacon/internal/web"
	"github.com/openlinkos/beacon/internal/web/link"
)

const defaultConfig = "/etc/beacon/config.yaml"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  beacon serve [-config path]                 run the service
  beacon check-config [-config path]          validate the configuration, its files and the database
  beacon config import DIR [-dry-run] [-note TEXT]
                                              make DIR/catalog, DIR/targets and DIR/targets/modules.yml
                                              the catalogue, probe targets and modules, as one change
  beacon config export DIR                    write them there in the same layout
  beacon config history [-limit 20] | config revert VERSION [-note TEXT]
  beacon setup                                print the one-time link that makes the first account, an owner
  beacon user list | user invite -role oncall [-note TEXT]
                                              people, and a one-time link that invites someone
  beacon user disable NAME | user enable NAME | user role NAME ROLE | user reset NAME
  beacon agent add NAME -owner USER [-preset oncall] [-machines a,b] [-expires 365d] [-ssh-key KEY|@FILE]
                                              an agent of USER and its token, with the commands that use it
  beacon agent list | agent remove NAME
  beacon token claim NAME -owner USER [-agent NAME] [-preset P]
                                              make a legacy token an agent of USER
  beacon token create NAME -scopes status:read,alerts:write [-expires 365d]
         [-ssh-key KEY|@FILE] [-machines a,b]   keys for the SSH channel; the machines it reaches
  beacon token list
  beacon token revoke NAME
  beacon token ssh-key NAME add KEY|@FILE | token ssh-key NAME remove FINGERPRINT
  beacon token machines NAME a,b|all
  beacon machine add NAME HOST[:PORT] [-user beacon] [-critical] [-note TEXT] [-replace]
                                              pin a machine's host key; prints what the machine needs
  beacon machine add NAME connector -key KEY|@FILE [-host-key KEY|@FILE] [...]
                                              a machine behind NAT, which connects out with ssh -R
  beacon machine connector NAME -key KEY|@FILE
                                              set or change the key it connects out with
  beacon machine list | machine remove NAME | machine ca
  beacon ssh pause | ssh resume | ssh status
                                              stop or allow agents' access to machines
  beacon silence create -matchers 'alertname=X,service=Y' -duration 4h -comment TEXT
                                              stop notifying alerts with these labels (or -alert GROUP_ID)
  beacon silence list | silence expire ID
  beacon audit verify [-from-seq N]
                                              check the audit log's hash chain from a checkpoint (default: all)
  beacon storage check                        the database's health: integrity, buckets, audit chain
  beacon enroll probe|host|heartbeat NAME [-rotate]
                                              a machine's gateway token (printed once, alone, on stdout)
  beacon drill fail 15m | drill off | drill status
  beacon healthcheck [-url URL]               exit 0 when the service answers (the image's health check)
  beacon version
Roles: `+strings.Join(store.Roles, ", ")+`; agents' presets: `+strings.Join(store.Presets, ", ")+`
Scopes (for machines' tokens and experts): `+strings.Join(store.Scopes, ", ")+`
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
	case "setup":
		return setupCmd(args[1:], stdout, stderr)
	case "user":
		return userCmd(args[1:], stdout, stderr)
	case "agent":
		return agentCmd(args[1:], stdout, stderr)
	case "drill":
		return drill(args[1:], stdout, stderr)
	case "enroll":
		return enroll(args[1:], stdout, stderr)
	case "machine":
		return machine(args[1:], stdout, stderr)
	case "ssh":
		return sshCmd(args[1:], stdout, stderr)
	case "silence":
		return silenceCmd(args[1:], stdout, stderr)
	case "audit":
		return auditCmd(args[1:], stdout, stderr)
	case "storage":
		return storageCmd(args[1:], stdout, stderr)
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
	fmt.Fprintf(stdout, "configuration OK: Feishu app %v, Pushover %v, Bark %v; gateway %q; SSH channel %s; %d agent hooks\n",
		cfg.Feishu.AppIDFile != "", cfg.Pushover.TokenFile != "", cfg.Bark.URLFile != "", cfg.Gateway.Listen, sshListen, len(cfg.Agents))
	fmt.Fprintf(stdout, "files: %s\n", inFiles)
	fmt.Fprintf(stdout, "database: %s\n", inDB)
	return 0
}

func serve(args []string, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	// Take SIGHUP from the first instant: a deployment that reloads right after starting the service
	// must not end it (the default action), so the signal waits in the channel until reloadOnHUP runs.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	path, _, ok := configFlag("serve", args, stderr)
	if !ok {
		return 2
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	so := storeOptions(cfg)
	// the move from the bbolt file, once: a database from before the SQL storage migrates in
	// whole or not at all, and the old file stays beside as status.db.pre-sql
	if migrated, err := store.Migrate(filepath.Join(cfg.DataDir, "status.db"), so); err != nil {
		log.Error("cannot start", "err", err)
		return 1
	} else if migrated {
		log.Info("the bbolt database moved into " + so.Path + "; the old file is kept as status.db.pre-sql")
	}
	st, err := store.OpenWith(so)
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
	m.Register("beacon_token_expiry_timestamp_seconds", "When an API or gateway token expires", "gauge", func() []metrics.Sample {
		var out []metrics.Sample
		for _, t := range st.Tokens() {
			out = append(out, metrics.Sample{Labels: metrics.Labels("token", t.Name, "subject", t.Subject), Value: float64(t.Expires.Unix())})
		}
		return out
	})

	// the audit log keeps what the retention says; the chain's checkpoints keep it verifiable
	pruneAudit := func() {
		if n, err := st.PruneAudit(cfg.Storage.Audit.Retention.D()); err != nil {
			log.Warn("the audit log could not be pruned", "err", err)
		} else if n > 0 {
			log.Info("the audit log was pruned", "entries", n)
		}
	}
	pruneAudit()
	pruneStop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-pruneStop:
				return
			case <-t.C:
				pruneAudit()
			}
		}
	}()
	defer close(pruneStop)

	var br *bridge.Bridge
	rcfg, err := routerConfig(cfg.Routing)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	rt := router.New(rcfg, st, sinkFunc{hook: func(ctx context.Context, w alertmanager.Webhook) error { return br.HandleWebhook(ctx, w) },
		watchdog: func() { br.Watchdog() }}, nil, log)
	m.Register("beacon_alerts_firing", "Firing alerts the router holds", "gauge", func() []metrics.Sample {
		return []metrics.Sample{{Value: float64(rt.Firing())}}
	})
	// the signed links to the pages people open from the chat, under a key made at first start
	pageKey, err := st.Secret("pages")
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	signer, err := link.NewSigner(pageKey, cfg.Pages.LinkTTL.D(), time.Now)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	pages := &link.Pages{Signer: signer, PublicURL: cfg.PublicURL}
	deps := bridge.Deps{
		Config: cfg, Store: st, AM: rt,
		Catalog: cdb.Catalog, Metrics: m, Log: log, Pages: pages,
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
	var board *probenow.Board // requests to probe on demand, shared by the bridge and the gateway
	if cfg.Gateway.Listen != "" {
		board = probenow.New(probenow.Deps{Catalog: cdb.Catalog, Targets: cdb.Targets, Metrics: m})
		deps.ProbeNow = board
	}
	gate, err := newGate(cfg, st, deps.Bot, pages, m, log)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	if gate != nil {
		deps.ShellCommands = shellCommands(gate)
		deps.SSHConfig = sshConfig(gate, cfg.SSH.PublicAddress)
		deps.Recheck = gate.Recheck
	}
	br = bridge.New(deps)
	announceSetup(br, st, log) // before the admin socket opens, so that beacon setup comes after it

	auth := &api.Auth{Store: st, IP: clientip.New(cfg.Proxies, false)}
	// Beacon's own authorization server, with which MCP clients sign in through the console (OAuth)
	oa := &oauth.Server{Store: st, Bridge: br, Auth: auth, PublicURL: cfg.PublicURL, Log: log, NoDocuments: !cfg.OAuth.Documents()}
	lang, _ := i18n.Parse(cfg.Language)
	// the public status pages, from the catalogue's status_pages, the probes, the incidents and maintenance
	sdeps := status.Deps{Config: cdb, Bridge: br, Store: st, PublicURL: cfg.PublicURL, Location: cfg.Location, Log: log}
	if cfg.VictoriaMetrics.URL != "" {
		sdeps.VM = vm.New(cfg.VictoriaMetrics.URL, &http.Client{Timeout: 30 * time.Second}) // a day of minutes per query
	}
	pagesOut := status.New(sdeps)
	sd := server.Deps{
		Bridge: br, API: &api.Server{Bridge: br, SSH: gate, SSHAddress: cfg.SSH.PublicAddress, Auth: auth, Log: log, Config: cdb, Status: pagesOut},
		MCP: mcpserver.Handler(br, mcpserver.SSH{Gate: gate, Address: cfg.SSH.PublicAddress}, auth, log, mcpserver.WithConfig(cdb),
			mcpserver.WithStatus(pagesOut), mcpserver.WithResourceMetadata(oa.ResourceMetadataURL)),
		Metrics: m, Drill: server.DrillFrom(st.Time), Log: log, OAuth: oa,
		Web: &web.Server{Bridge: br, SSH: gate, Auth: auth, Pages: pages, Lang: lang, Location: cfg.Location, Log: log,
			Store: st, Config: cdb, Hooks: cfg.Agents, Version: version.Version, Status: pagesOut, Branding: cfg.Branding, OAuth: oa}, // the public pages at /, the console at /console
	}
	if cfg.Gateway.Listen != "" {
		sd.Web.Reporting = web.ReportingFrom(m) // when each probe location last wrote through the gateway
	}
	if deps.Feishu != nil && cfg.Feishu.Callback == "http" {
		sd.Feishu = feishu.HTTPHandler(sec.FeishuVerificationToken, sec.FeishuEncryptKey, br.HandleAction)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reloadOnHUP(ctx, hup, cfg, cdb, log)
	if gate != nil {
		go func() {
			gate.RetryPending(ctx)
			t := time.NewTicker(time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					gate.RetryPending(ctx)
					gate.TidyLocal(cfg.SSH.Keep.D())
				}
			}
		}()
	}

	// The leader lease of a primary/standby pair (PostgreSQL): one instance holds the Feishu long
	// connection, the alert routing, the escalation loops and the SSH channel, and answers /healthz
	// with 200; the other waits, answers /livez with 200 while it can reach the database, and takes
	// over when the lease expires. What the pair shares is in the database (and, for recordings, in
	// S3-compatible storage); a loop restarts from it as after a restart.
	var lease *ha.Lease
	single := func(fn func(context.Context)) {
		go fn(ctx)
	}
	if cfg.HA.Enabled {
		pool, err := ha.OpenPool(sec.StorageDSN, cfg.HA.Expire.D())
		if err != nil {
			log.Error("cannot start", "err", err)
			return 1
		}
		defer func() { _ = pool.Close() }()
		if lease, err = ha.Open(pool, ha.Options{Name: cfg.HA.Name, ID: cfg.HA.ID, Renew: cfg.HA.Renew.D(), Expire: cfg.HA.Expire.D(), Log: log}); err != nil {
			log.Error("cannot start", "err", err)
			return 1
		}
		st.SetWriteGuard(lease.CheckWrite)
		go lease.Run(ctx)
		single = func(fn func(context.Context)) {
			go ha.Single(ctx, lease, fn)
		}
		// the standby's token cache and catalogue are a snapshot of its start; refresh on taking
		// over, and while it waits. Writes check the lease row inside the transaction.
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if !lease.Leader() {
						cdb.Reload()
					}
					if err := st.RefreshTokens(); err != nil {
						log.Warn("token cache was not refreshed", "err", err)
					}
				}
			}
		}()
		sd.Standby = func() bool { return !lease.Leader() }
		sd.Live = lease.Healthy
		log.Info("high availability on: this instance answers /healthz with 503 until it holds the leader lease; /livez stays 200 while it can reach the database",
			"lease", cfg.HA.Name, "id", lease.ID(), "renew", cfg.HA.Renew.String(), "expire", cfg.HA.Expire.String())
	}
	if gate != nil && lease != nil {
		gate.SetLeader(lease.Leader)
	}
	if listener != nil {
		single(func(lctx context.Context) {
			if err := listener.Run(lctx); err != nil && lctx.Err() == nil {
				log.Error("feishu long connection stopped", "err", err)
			}
		})
	}
	var ping func(context.Context, bool) error
	if sec.HealthchecksBridgeURL != "" {
		ping = func(ctx context.Context, ok bool) error { return notify.Ping(ctx, nil, sec.HealthchecksBridgeURL, ok) }
	}
	single(func(lctx context.Context) { br.Run(lctx, ping) })
	go pagesOut.Run(ctx)
	single(func(lctx context.Context) {
		cdb.Reload()
		if err := st.RefreshTokens(); err != nil {
			log.Warn("token cache was not refreshed", "err", err)
		}
		rt.Restore(st.Groups())
		rt.Run(lctx)
	})
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
		silences := ctl.SilenceHandler(br)
		socket.Handle("/v1/silences", silences)
		socket.Handle("/v1/silences/", silences)
		people := ctl.PeopleHandler(br)
		for _, p := range []string{"/v1/setup", "/v1/users", "/v1/users/", "/v1/agents", "/v1/agents/", "/v1/claims"} {
			socket.Handle(p, people)
		}
		socket.Handle("/", ctl.Handler(st, active, machines))
		cs := &http.Server{Handler: socket, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = cs.Serve(ln) }()
		defer func() { _ = cs.Close() }()
	} else if !errors.Is(err, errors.ErrUnsupported) {
		log.Warn("admin socket unavailable; token, machine, silence and drill commands will not work", "err", err)
	}

	hs := &http.Server{Addr: cfg.Listen, Handler: server.Handler(sd), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute} // probe_now waits up to 90s
	errc := make(chan error, 3)
	go func() { errc <- hs.ListenAndServe() }()
	var gs *http.Server
	if cfg.Gateway.Listen != "" {
		vmURL, _ := url.Parse(cfg.VictoriaMetrics.URL)
		gdeps := gateway.Deps{Auth: auth, Catalog: cdb.Catalog, Targets: cdb.Targets,
			Modules: cdb.Modules, VM: vmURL, Alerts: rt.Ingest, Changes: br.RecordChange, RulesToken: sec.RulesToken,
			Metrics: m, Log: log, Requests: board}
		if lease != nil {
			gdeps.Leader = lease.Leader
		}
		gs = &http.Server{Addr: cfg.Gateway.Listen, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute,
			WriteTimeout: 2 * time.Minute, IdleTimeout: 5 * time.Minute,
			Handler: gateway.Handler(gdeps)}
		go func() { errc <- gs.ListenAndServe() }()
	}
	if gate != nil {
		single(func(lctx context.Context) {
			ln, err := (&net.ListenConfig{}).Listen(lctx, "tcp", cfg.SSH.Listen)
			if err != nil {
				if lctx.Err() == nil {
					errc <- fmt.Errorf("ssh.listen: %w", err)
				}
				return
			}
			go func() {
				<-lctx.Done()
				_ = ln.Close()
			}()
			if err := gate.Serve(ln); err != nil && lctx.Err() == nil {
				errc <- err
			}
		})
		single(func(lctx context.Context) { gate.Tidy(lctx) })
	}
	log.Info("beacon started", "version", version.Version, "listen", cfg.Listen,
		"feishu_app", deps.Feishu != nil, "callback", cfg.Feishu.Callback, "pushover", deps.Pushover != nil, "locations", len(cdb.Catalog().Locations),
		"gateway", cfg.Gateway.Listen, "targets", cdb.Targets().Count(), "config_version", cdb.Version(),
		"ssh", map[bool]string{true: cfg.SSH.Listen, false: "off"}[gate != nil], "agent_hooks", len(cfg.Agents))
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
	log.Info("beacon stopped")
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
	dataDir := os.Getenv("BEACON_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/beacon"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := ctl.Dial(ctx, ctl.Path(dataDir))
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			fmt.Fprintf(stderr, "cannot reach the running service at %s: %v\n(run this inside the container: docker exec beacon beacon …)\n", ctl.Path(dataDir), err)
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
		fmt.Fprintln(tw, "NAME\tSCOPES\tEXPIRES\tLIFETIME\tRENEWED\tCREATED BY")
		for _, t := range list {
			renewed := "-"
			if !t.Renewed.IsZero() {
				renewed = t.Renewed.Local().Format(time.DateOnly)
			}
			scopes := strings.Join(t.Scopes, ",")
			if t.Agent != "" { // what it may do is its agent's: beacon agent list
				scopes = "agent " + t.Agent + "'s"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, scopes, t.Expires.Format(time.DateOnly), t.Lifetime, renewed, t.CreatedBy)
		}
		_ = tw.Flush()
		return 0
	case "ssh-key", "machines":
		return tokenSSH(args, stdout, stderr)
	case "claim":
		return tokenClaim(args[1:], stdout, stderr)
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
	url := fs.String("url", "http://127.0.0.1:17001/livez", "address to check")
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
