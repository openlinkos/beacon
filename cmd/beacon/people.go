package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
)

// People and their agents on the command line, through the admin socket: the first account's setup
// link, invitations, roles, disabling, reset links; agents with their first token and the snippets that
// use it; claiming a legacy token. The links and the secret are printed once, on stdout; the rest goes
// to stderr.

const userUsage = `user list
user invite -role viewer|oncall|operator|owner [-note TEXT]
user disable NAME | user enable NAME
user role NAME viewer|oncall|operator|owner
user reset NAME`

const agentUsage = `agent add NAME -owner USER [-preset readonly|oncall|operator] [-machines a,b] [-expires 365d] [-ssh-key KEY|@FILE]
agent add NAME -owner USER -scopes a,b    (custom scopes, still within USER's role)
agent list
agent remove NAME`

// announceSetup writes, while Beacon has no account, a one-time link to make the first one (an owner,
// valid 30 minutes) to the log, and says how many legacy tokens wait to be claimed.
func announceSetup(br *bridge.Bridge, st *store.Store, log *slog.Logger) {
	if n := len(br.LegacyTokens()); n > 0 {
		log.Info("legacy tokens keep working; an owner can claim each as an agent of a person on the console's Tokens page or with beacon token claim", "legacy_tokens", n)
	}
	if st.HasAccounts() {
		return
	}
	l, err := br.SetupLink("start")
	if err != nil {
		log.Warn("cannot make the setup link", "err", err)
		return
	}
	log.Warn("Beacon has no accounts yet: open this one-time link within 30 minutes to make the first, an owner; beacon setup prints a new one",
		"setup_link", l.URL, "expires", l.Expires.Format(time.RFC3339))
}

// sshConfig is the ssh configuration for a principal: a block for each machine it reaches and the
// channel's known_hosts line; empty without the channel's public address.
func sshConfig(gate *sshgate.Gate, address string) func(string) string {
	return func(key string) string {
		if address == "" {
			return ""
		}
		l, err := gate.List(sshgate.Actor{Key: key}, address)
		if err != nil || l.SSHConfig == "" {
			return ""
		}
		return "# ~/.ssh/config\n" + l.SSHConfig + "\n\n# ~/.ssh/known_hosts\n" + l.KnownHosts
	}
}

// setupCmd prints the one-time link that creates the first account, an owner.
func setupCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "setup")
		return 2
	}
	c, ok := dial(stderr)
	if !ok {
		return 1
	}
	l, err := c.SetupLink(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stderr, "Open this link within 30 minutes (until %s) to make the first account, an owner. It works once:\n", l.Expires.Local().Format(time.DateTime))
	fmt.Fprintln(stdout, l.URL)
	if strings.HasPrefix(l.URL, "/") {
		fmt.Fprintln(stderr, "(public_url is not set: put Beacon's own address in front of the path)")
	}
	return 0
}

func userCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, userUsage)
		return 2
	}
	ctx := context.Background()
	c, ok := dial(stderr)
	if !ok {
		return 1
	}
	switch args[0] {
	case "list":
		out, err := c.Users(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "USERNAME\tDISPLAY NAME\tROLE\tSTATUS\tLAST SIGN-IN\tAGENTS")
		for _, u := range out.Users {
			status, last := "enabled", "-"
			if u.Disabled {
				status = "disabled"
			}
			if !u.LastSignIn.IsZero() {
				last = u.LastSignIn.Local().Format(time.DateTime)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", u.Username, u.DisplayName, u.Role, status, last, strings.Join(u.Agents, ","))
		}
		_ = tw.Flush()
		if len(out.Invitations) > 0 {
			fmt.Fprintf(stdout, "\n%d invitations not yet used:\n", len(out.Invitations))
			for _, l := range out.Invitations {
				fmt.Fprintf(stdout, "  %s as %s, by %s, until %s %s\n", l.ID, l.Role, l.CreatedBy, l.Expires.Local().Format(time.DateTime), l.Note)
			}
		}
		return 0
	case "invite":
		fs := flag.NewFlagSet("user invite", flag.ContinueOnError)
		fs.SetOutput(stderr)
		role := fs.String("role", "", "what the person may do: "+strings.Join(store.Roles, ", "))
		note := fs.String("note", "", "who the invitation is for, as the list shows it")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *role == "" {
			fmt.Fprintln(stderr, "user invite -role viewer|oncall|operator|owner [-note TEXT]")
			return 2
		}
		l, err := c.Invite(ctx, ctl.InviteRequest{Role: *role, Note: *note})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stderr, "An invitation to join as %s, until %s. Send the link to that person only; it works once:\n", l.Role, l.Expires.Local().Format(time.DateTime))
		fmt.Fprintln(stdout, l.URL)
		return 0
	case "disable", "enable":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "user "+args[0]+" NAME")
			return 2
		}
		disabled := args[0] == "disable"
		u, err := c.UpdateUser(ctx, args[1], bridge.UserChange{Disabled: &disabled})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s is %sd\n", u.Username, args[0])
		return 0
	case "role":
		if len(args) != 3 {
			fmt.Fprintln(stderr, "user role NAME viewer|oncall|operator|owner")
			return 2
		}
		u, err := c.UpdateUser(ctx, args[1], bridge.UserChange{Role: &args[2]})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s is %s now; their sessions and agents follow at once\n", u.Username, u.Role)
		return 0
	case "reset":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "user reset NAME")
			return 2
		}
		l, err := c.ResetUser(ctx, args[1])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stderr, "With this link %s chooses a new password, once, until %s; their sign-ins end then:\n", l.User, l.Expires.Local().Format(time.DateTime))
		fmt.Fprintln(stdout, l.URL)
		return 0
	}
	fmt.Fprintln(stderr, userUsage)
	return 2
}

func agentCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, agentUsage)
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("agent add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		owner := fs.String("owner", "", "the person the agent acts for")
		preset := fs.String("preset", "", "what it may do: "+strings.Join(store.Presets, ", ")+"; the strongest the owner's role allows by default")
		scopes := fs.String("scopes", "", "comma-separated scopes instead of a preset (custom), within the owner's role")
		machines := fs.String("machines", "", "comma-separated machines it reaches through the SSH channel; all when empty")
		expires := fs.String("expires", "365d", "its token's lifetime, such as 90d or 720h")
		sshKey := fs.String("ssh-key", "", "a public key for the SSH channel (an authorized_keys line), or @FILE")
		pos, ok := positional(fs, args[1:])
		if !ok || len(pos) != 1 || *owner == "" {
			fmt.Fprintln(stderr, agentUsage)
			return 2
		}
		req := ctl.AgentRequest{Name: pos[0], Owner: *owner, Preset: *preset, TTL: *expires}
		if *scopes != "" {
			if *preset != "" && *preset != store.PresetCustom {
				fmt.Fprintln(stderr, "give -preset or -scopes, not both")
				return 2
			}
			req.Preset, req.Scopes = store.PresetCustom, strings.Split(*scopes, ",")
		}
		if *machines != "" {
			req.Machines = strings.Split(*machines, ",")
		}
		if *sshKey != "" {
			k, err := keyArg(*sshKey)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			req.SSHKeys = []string{k}
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		out, err := c.AddAgent(ctx, req)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stderr, "agent %s (%s, may do %s) made; its token %s expires %s. The secret is shown only now:\n",
			out.Agent.Actor, out.Agent.Preset, strings.Join(out.Agent.Scopes, ","), out.Token.Name, out.Token.Expires.Local().Format(time.DateOnly))
		fmt.Fprintln(stdout, out.Secret)
		for _, sn := range out.Snippets {
			fmt.Fprintf(stderr, "\n%s:\n%s\n", sn.Title, sn.Text)
		}
		return 0
	case "list":
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		list, err := c.Agents(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "AGENT\tOWNER\tPRESET\tMAY DO\tMACHINES\tTOKENS\tOAUTH\tKEYS")
		for _, a := range list {
			m := "all"
			if len(a.Machines) > 0 {
				m = strings.Join(a.Machines, ",")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\n", a.Name, a.Owner, a.Preset, strings.Join(a.Scopes, ","), m, len(a.Tokens), len(a.Grants), len(a.SSHKeys))
		}
		_ = tw.Flush()
		return 0
	case "remove":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "agent remove NAME")
			return 2
		}
		c, ok := dial(stderr)
		if !ok {
			return 1
		}
		if err := c.RemoveAgent(ctx, args[1]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "agent %s removed, with its tokens, OAuth sign-ins and keys\n", args[1])
		return 0
	}
	fmt.Fprintln(stderr, agentUsage)
	return 2
}

// tokenClaim makes a legacy token an agent of a person.
func tokenClaim(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token claim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	owner := fs.String("owner", "", "the person the agent acts for")
	agent := fs.String("agent", "", "the agent's name; the token's own by default")
	preset := fs.String("preset", "", "readonly, oncall, operator or custom (keep its scopes); the token's closest by default")
	pos, ok := positional(fs, args)
	if !ok || len(pos) != 1 || *owner == "" {
		fmt.Fprintln(stderr, "token claim NAME -owner USER [-agent NAME] [-preset readonly|oncall|operator|custom]")
		return 2
	}
	c, ok := dial(stderr)
	if !ok {
		return 1
	}
	a, err := c.Claim(context.Background(), ctl.ClaimRequest{Token: pos[0], Owner: *owner, Agent: *agent, Preset: *preset})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "token %s is agent %s's now (%s, may do %s); it keeps its secret and expiry\n", pos[0], a.Actor, a.Preset, strings.Join(a.Scopes, ","))
	return 0
}
