package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/metrics"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
)

// sshKeys parses the SSH channel's host key and certificate authority from their files.
func sshKeys(cfg *config.Config) (host, ca ssh.Signer, err error) {
	if host, err = ssh.ParsePrivateKey([]byte(cfg.Secrets.SSHHostKey)); err != nil {
		return nil, nil, fmt.Errorf("ssh.host_key_file: %w (make one with ssh-keygen -t ed25519 -N '' -f FILE)", err)
	}
	if ca, err = ssh.ParsePrivateKey([]byte(cfg.Secrets.SSHCAKey)); err != nil {
		return nil, nil, fmt.Errorf("ssh.ca_key_file: %w (make one with ssh-keygen -t ed25519 -N '' -f FILE)", err)
	}
	if string(host.PublicKey().Marshal()) == string(ca.PublicKey().Marshal()) {
		return nil, nil, errors.New("ssh: the host key and the certificate authority must be different keys")
	}
	return host, ca, nil
}

// newGate builds the SSH channel when the configuration turns it on; nil otherwise.
func newGate(cfg *config.Config, st *store.Store, bot sshgate.Poster, m *metrics.Metrics, log *slog.Logger) (*sshgate.Gate, error) {
	if cfg.SSH.CAKeyFile == "" {
		return nil, nil
	}
	host, ca, err := sshKeys(cfg)
	if err != nil {
		return nil, err
	}
	lang := i18n.ZH
	if cfg.Language == "en" {
		lang = i18n.EN
	}
	return sshgate.New(sshgate.Deps{Store: st, HostKey: host, CA: ca, SourceAddresses: cfg.SSH.SourceAddresses,
		Dir: filepath.Join(cfg.DataDir, "recordings"), Keep: cfg.SSH.Keep.D(), Bot: bot, Lang: lang, Metrics: m, Log: log,
		PublicAddress: cfg.SSH.PublicAddress})
}

// keyArg is a public key given on the command line, or @FILE to read it from a file.
func keyArg(s string) (string, error) {
	if path, ok := strings.CutPrefix(s, "@"); ok {
		b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
		return strings.TrimSpace(string(b)), err
	}
	return s, nil
}

// positional splits args into flags (parsed by fs) and positional arguments, in any order.
func positional(fs *flag.FlagSet, args []string) ([]string, bool) {
	var pos []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return nil, false
		}
		rest = fs.Args()
		if len(rest) > 0 {
			pos, rest = append(pos, rest[0]), rest[1:]
		}
	}
	return pos, true
}

func machine(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("machine add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		user := fs.String("user", "beacon", "the account on the machine, the principal of Beacon's certificates")
		critical := fs.Bool("critical", false, "mark the machine critical, for whoever works on it")
		note := fs.String("note", "", "what the machine is")
		replace := fs.Bool("replace", false, "pin the host key of a machine that is added already again")
		key := fs.String("key", "", "connector machines: the public key the machine connects out with (an authorized_keys line), or @FILE")
		hostKey := fs.String("host-key", "", "connector machines: the machine's host key (its /etc/ssh/ssh_host_ed25519_key.pub), or @FILE; learned at its first connection otherwise")
		pos, ok := positional(fs, args[1:])
		if !ok {
			return 2
		}
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "machine add NAME HOST[:PORT]|connector [-user beacon] [-critical] [-note TEXT] [-replace] [-key KEY|@FILE] [-host-key KEY|@FILE]")
			return 2
		}
		ck, err := keyArg(*key)
		if err == nil {
			*hostKey, err = keyArg(*hostKey)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		a, err := c.AddMachine(ctx, sshgate.MachineInput{Name: pos[0], Address: pos[1], User: *user, Critical: *critical, Note: *note, Replace: *replace,
			ConnectorKey: ck, HostKey: *hostKey})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if a.Connector != nil {
			printConnector(stdout, a)
			return 0
		}
		fmt.Fprintf(stdout, "added %s at %s as %s; its host key is %s\n", a.Machine.Name, a.Machine.Address, a.Machine.User, a.Machine.HostKey)
		fmt.Fprintf(stdout, "On the machine, as root, if not done yet:\n"+
			"  echo '%s' > /etc/ssh/beacon_ca.pub\n"+
			"  echo 'TrustedUserCAKeys /etc/ssh/beacon_ca.pub' > /etc/ssh/sshd_config.d/beacon.conf\n"+
			"  id %s >/dev/null 2>&1 || useradd -m -s /bin/bash -p '*' %s\n"+
			"  systemctl reload ssh\n(see docs/ssh.md for sudo and for PerSourcePenaltyExemptList)\n", a.TrustedUserCAKey, a.Machine.User, a.Machine.User)
		return 0
	case "connector":
		fs := flag.NewFlagSet("machine connector", flag.ContinueOnError)
		fs.SetOutput(stderr)
		key := fs.String("key", "", "the public key the machine connects out with (an authorized_keys line), or @FILE")
		pos, ok := positional(fs, args[1:])
		if !ok {
			return 2
		}
		if len(pos) != 1 || *key == "" {
			fmt.Fprintln(stderr, "machine connector NAME -key KEY|@FILE")
			return 2
		}
		k, err := keyArg(*key)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		v, err := c.SetMachineConnector(ctx, pos[0], k)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "machine %s connects out with the key %s; %s\n", v.Name, fingerprintOf(k), connectedText(v))
		return 0
	case "list":
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		l, err := c.Machines(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tADDRESS\tUSER\tHOST KEY\tADDED\tNOTE")
		for _, m := range l.Machines {
			name, addr, hk := m.Name, m.Address, m.HostKey
			if m.Critical {
				name += " (critical)"
			}
			if m.Connector {
				addr += " (" + connectedText(m) + ")"
				if hk == "" {
					hk = "(learned at its first connection)"
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", name, addr, m.User, hk, m.Added.Local().Format(time.DateOnly), m.Note)
		}
		_ = tw.Flush()
		return 0
	case "remove":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "machine remove NAME")
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		if err := c.RemoveMachine(ctx, args[1]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "machine %s removed\n", args[1])
		return 0
	case "ca":
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		st, err := c.SSH(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !st.Enabled {
			fmt.Fprintln(stderr, "the SSH channel is off: set ssh.ca_key_file and ssh.host_key_file in the configuration")
			return 1
		}
		fmt.Fprintln(stdout, st.TrustedUserCAKey)
		return 0
	}
	usage(stderr)
	return 2
}

// connectedText says whether a connector machine is connected, and since when.
func connectedText(m sshgate.MachineView) string {
	if m.Connected == nil || !*m.Connected {
		return "disconnected"
	}
	return "connected since " + m.ConnectedSince.Local().Format(time.DateTime)
}

// fingerprintOf is the SHA256 fingerprint of a public key line, or the line itself when it does not parse.
func fingerprintOf(line string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return line
	}
	return ssh.FingerprintSHA256(k)
}

// printConnector prints what a machine behind NAT needs on its side.
func printConnector(stdout io.Writer, a sshgate.Added) {
	hk := "its host key is " + a.Machine.HostKey
	if a.Connector.HostKeyPending {
		hk = "its host key will be pinned when it first connects (the audit log and the chat will say so; -host-key pins it now)"
	}
	fmt.Fprintf(stdout, "added %s as a connector machine (user %s): it connects out to Beacon with ssh -R; %s\n", a.Machine.Name, a.Machine.User, hk)
	fmt.Fprintf(stdout, "On the machine, as root (idempotent; see docs/ssh.md, Machines behind NAT):\n")
	for _, line := range strings.Split(strings.TrimRight(a.Connector.Script, "\n"), "\n") {
		fmt.Fprintf(stdout, "  %s\n", line)
	}
}

func sshCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "pause" && args[0] != "resume" && args[0] != "status") {
		fmt.Fprintln(stderr, "ssh pause | ssh resume | ssh status")
		return 2
	}
	c, ok := dial(stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	var st ctl.SSHState
	var err error
	if args[0] == "status" {
		st, err = c.SSH(ctx)
	} else {
		st, err = c.PauseSSH(ctx, args[0] == "pause")
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch {
	case !st.Enabled:
		fmt.Fprintln(stdout, "the SSH channel is off")
	case !st.PausedSince.IsZero():
		fmt.Fprintf(stdout, "agents' access to machines is paused since %s\n", st.PausedSince.Local().Format(time.DateTime))
	default:
		fmt.Fprintf(stdout, "agents reach machines; the channel's host key is %s\n", st.HostKey)
	}
	return 0
}

// tokenSSH handles `token ssh-key NAME add KEY|@FILE`, `token ssh-key NAME remove FINGERPRINT` and
// `token machines NAME a,b|all`.
func tokenSSH(args []string, stdout, stderr io.Writer) int {
	ctx := context.Background()
	var out ctl.TokenInfo
	var err error
	switch {
	case args[0] == "ssh-key" && len(args) == 4 && (args[2] == "add" || args[2] == "remove"):
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		if args[2] == "add" {
			key, kerr := keyArg(args[3])
			if kerr != nil {
				fmt.Fprintln(stderr, kerr)
				return 1
			}
			out, err = c.AddSSHKey(ctx, args[1], key)
		} else {
			out, err = c.RemoveSSHKey(ctx, args[1], args[3])
		}
	case args[0] == "machines" && len(args) == 3:
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		out, err = c.SetTokenMachines(ctx, args[1], strings.Split(args[2], ","))
	default:
		fmt.Fprintln(stderr, "token ssh-key NAME add KEY|@FILE | token ssh-key NAME remove FINGERPRINT | token machines NAME a,b|all")
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	machines := strings.Join(out.Machines, ",")
	if machines == "" {
		machines = "all"
	}
	fmt.Fprintf(stdout, "token %s: SSH keys %s; machines %s\n", out.Name, strings.Join(out.SSHKeys, " "), machines)
	return 0
}
