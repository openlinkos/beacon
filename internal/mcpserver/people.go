package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/ctl"
	"github.com/openlinkos/beacon/internal/store"
)

// People and the caller's SSH keys, as the REST routes under /api/v1/users and /api/v1/ssh-keys offer
// them: list_users, invite_user and withdraw_link for an owner's credential (accounts:write);
// list_ssh_keys, add_ssh_key and remove_ssh_key for any credential with machines:ssh, on its own keys.
// Password resets and role changes are not offered here: they are for people, on the console or the
// host. Withdrawing a pending link only takes access away, so it is.

type (
	usersOut struct {
		Users       []bridge.UserView `json:"users"`
		Invitations []bridge.LinkView `json:"invitations"`
		ResetLinks  []bridge.LinkView `json:"reset_links"`
	}
	withdrawLinkIn struct {
		ID string `json:"id" jsonschema:"the pending invitation's or password reset link's id, as list_users gives it"`
	}
	inviteIn struct {
		Role string `json:"role" jsonschema:"what the person may do: viewer, oncall, operator or owner"`
		Note string `json:"note,omitempty" jsonschema:"who the invitation is for, as people will see it on the console"`
	}
	keysOut struct {
		Keys []bridge.SSHKeyView `json:"keys"`
	}
	addKeyIn struct {
		Key     string `json:"key" jsonschema:"your public key, an authorized_keys line (ssh-ed25519 AAAA... comment), such as the content of ~/.ssh/id_ed25519.pub; never the private key"`
		Comment string `json:"comment,omitempty" jsonschema:"what the key is, such as the machine and harness it is on; the key line's own comment otherwise"`
	}
	removeKeyIn struct {
		Fingerprint string `json:"fingerprint" jsonschema:"the key's SHA256 fingerprint, as list_ssh_keys gives it"`
	}
	okKeyOut struct {
		OK          bool   `json:"ok"`
		Fingerprint string `json:"fingerprint"`
	}
	listAgentsIn struct {
		Owner string `json:"owner,omitempty" jsonschema:"only this owner's agents; omitted returns your own agents or all agents for an administrator"`
	}
	agentsOut struct {
		Agents []bridge.AgentView `json:"agents"`
	}
	agentNameIn struct {
		Name string `json:"name" jsonschema:"the agent's name"`
	}
	createAgentIn struct {
		Name          string   `json:"name" jsonschema:"the agent's name: 2 to 40 lower-case letters, digits, dots, dashes and underscores"`
		Owner         string   `json:"owner" jsonschema:"whose agent; a person makes agents for themself when this is left out"`
		Preset        string   `json:"preset" jsonschema:"readonly, oncall, operator or custom; the strongest preset within the owner's role when left out"`
		Scopes        []string `json:"scopes,omitempty" jsonschema:"custom only: people's scopes, at least one, within the owner's role and within what you yourself may do"`
		MachineAccess *string  `json:"machine_access,omitempty" jsonschema:"all (every machine that is not critical, machines added later included; only a person gives it) or named (only the machines listed); left out, named"`
		Machines      []string `json:"machines,omitempty" jsonschema:"the machines named; with named and none listed, or left out, it reaches no machine. A critical machine is reached only when named, and only an owner names one"`
		Expires       string   `json:"expires,omitempty" jsonschema:"the first token's lifetime, such as 90d or 12h; left out, no token"`
	}
	updateAgentIn struct {
		Name          string    `json:"name" jsonschema:"the agent's name"`
		Preset        *string   `json:"preset,omitempty" jsonschema:"readonly, oncall, operator or custom; left out, it stays"`
		Scopes        []string  `json:"scopes,omitempty" jsonschema:"custom only; left out, the scopes stay. An empty list is refused"`
		MachineAccess *string   `json:"machine_access,omitempty" jsonschema:"all or named; with machines left out too, the machine access stays"`
		Machines      *[]string `json:"machines,omitempty" jsonschema:"the machines named; an empty list names none, which reaches no machine"`
	}
)

func addPeopleTools(s *mcp.Server, b *bridge.Bridge) {
	registerTool(s, &mcp.Tool{Name: "list_users", Title: "List people", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)},
		Description: "The people who sign in to Beacon (username, display name, role, last sign-in, whether disabled, their agents), and the invitations and password reset links not yet used (never their codes). Needs a standalone token with accounts:write."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, usersOut, error) {
			who, err := need(ctx, "accounts:write")
			if err != nil {
				return nil, usersOut{}, err
			}
			users, invs, err := b.Users(who)
			if err != nil {
				return nil, usersOut{}, err
			}
			resets, err := b.ResetLinks(who)
			return nil, usersOut{Users: users, Invitations: invs, ResetLinks: resets}, err
		})

	registerTool(s, &mcp.Tool{Name: "invite_user", Title: "Invite a person",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), OpenWorldHint: t(false)},
		Description: "Make a one-time link, valid for 24 hours, with which a person joins Beacon with a role, choosing their username and password. Hand the link to that person only. Needs a standalone token with accounts:write and every scope the role gives; the link works only while the token still could make it. Recorded and echoed to the alert chat (without the link)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in inviteIn) (*mcp.CallToolResult, bridge.LinkView, error) {
			who, err := need(ctx, "accounts:write")
			if err != nil {
				return nil, bridge.LinkView{}, err
			}
			l, err := b.Invite(ctx, who, in.Role, in.Note)
			return nil, l, err
		})

	registerTool(s, &mcp.Tool{Name: "withdraw_link", Title: "Withdraw a pending link",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(true), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "Withdraw a pending invitation or password reset link by its id (list_users), so that it stops working at once. Needs a standalone token with accounts:write. Recorded and echoed to the alert chat."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in withdrawLinkIn) (*mcp.CallToolResult, bridge.LinkView, error) {
			who, err := need(ctx, "accounts:write")
			if err != nil {
				return nil, bridge.LinkView{}, err
			}
			l, err := b.WithdrawLink(ctx, who, in.ID)
			return nil, l, err
		})

	registerTool(s, &mcp.Tool{Name: "list_ssh_keys", Title: "Your SSH keys", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)},
		Description: "The public keys you sign in to Beacon's SSH channel with, for your own ssh, scp, sftp and rsync."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, keysOut, error) {
			who, err := need(ctx, "machines:ssh")
			if err != nil {
				return nil, keysOut{}, err
			}
			keys, err := b.SSHKeys(who, "")
			return nil, keysOut{Keys: keys}, err
		})

	registerTool(s, &mcp.Tool{Name: "add_ssh_key", Title: "Register your SSH key",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "Register your public key for Beacon's SSH channel, so that your own ssh, scp, sftp and rsync reach the machines you may reach (list_machines gives the ssh_config block). Give the public key only; your private key never leaves your machine. A key belongs to one agent. Shown to the person you act for and echoed to the alert chat."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in addKeyIn) (*mcp.CallToolResult, bridge.SSHKeyView, error) {
			who, err := need(ctx, "machines:ssh")
			if err != nil {
				return nil, bridge.SSHKeyView{}, err
			}
			k, err := b.AddSSHKey(ctx, who, "", in.Key, in.Comment)
			return nil, k, err
		})

	registerTool(s, &mcp.Tool{Name: "remove_ssh_key", Title: "Remove your SSH key",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(true), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "Remove one of your public keys from Beacon's SSH channel, by its fingerprint. Echoed to the alert chat."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in removeKeyIn) (*mcp.CallToolResult, okKeyOut, error) {
			who, err := need(ctx, "machines:ssh")
			if err != nil {
				return nil, okKeyOut{}, err
			}
			if err := b.RemoveSSHKey(ctx, who, "", in.Fingerprint); err != nil {
				return nil, okKeyOut{}, err
			}
			return nil, okKeyOut{OK: true, Fingerprint: in.Fingerprint}, nil
		})

	registerTool(s, &mcp.Tool{Name: "list_agents", Title: "List agents",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)},
		Description: "List delegated agents you own (or all agents for a standalone owner credential), with their preset, effective scopes, machines, tokens, OAuth grants and SSH keys."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in listAgentsIn) (*mcp.CallToolResult, agentsOut, error) {
			p, err := caller(ctx)
			if err != nil {
				return nil, agentsOut{}, err
			}
			agents, err := b.Agents(bridge.ActorOf(p, "mcp"), in.Owner)
			return nil, agentsOut{Agents: agents}, err
		})

	registerTool(s, &mcp.Tool{Name: "get_agent", Title: "Get an agent",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)},
		Description: "Inspect one delegated agent's preset, wanted and effective scopes, machines, tokens, OAuth grants and SSH keys. Requires being the agent's owner or a standalone owner credential."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in agentNameIn) (*mcp.CallToolResult, bridge.AgentView, error) {
			p, err := caller(ctx)
			if err != nil {
				return nil, bridge.AgentView{}, err
			}
			v, err := b.Agent(bridge.ActorOf(p, "mcp"), in.Name)
			return nil, v, err
		})

	registerTool(s, &mcp.Tool{Name: "create_agent", Title: "Make an agent",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), OpenWorldHint: t(false)},
		Description: "Make an agent for a person, and a token when expires is set (such as 90d). The secret is shown once. preset is readonly, oncall, operator or custom. Custom scopes are people's scopes, only for a person or a legacy token with accounts:write (never an agent), within the owner's role and within what you yourself may do. You never give an agent a scope or a machine you do not hold; only a person gives access to all machines, and only an owner names a critical machine. Without machine_access and machines it reaches no machine. Recorded and echoed to the alert chat."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in createAgentIn) (*mcp.CallToolResult, bridge.AgentCreated, error) {
			p, err := caller(ctx)
			if err != nil {
				return nil, bridge.AgentCreated{}, err
			}
			who := bridge.ActorOf(p, "mcp")
			agent := bridge.AgentInput{Name: in.Name, Owner: in.Owner, Preset: in.Preset}
			names := in.Machines
			machines, err := bridge.MachineChoice(in.MachineAccess, &names)
			if err != nil {
				return nil, bridge.AgentCreated{}, err
			}
			agent.Machines = *machines
			if in.Preset == store.PresetCustom {
				agent.Scopes = in.Scopes
				if agent.Scopes == nil {
					agent.Scopes = []string{}
				}
			}
			if in.Expires != "" {
				d, err := ctl.ParseTTL(in.Expires)
				if err != nil {
					return nil, bridge.AgentCreated{}, err
				}
				agent.TTL = d
			}
			out, err := b.CreateAgent(ctx, who, agent)
			return nil, out, err
		})

	registerTool(s, &mcp.Tool{Name: "update_agent", Title: "Change an agent",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), IdempotentHint: true, OpenWorldHint: t(false)},
		Description: "Change an agent's preset, custom scopes or machines. A field left out stays. Custom scopes follow the same rule as create_agent. You never give an agent a scope or a machine you do not hold; only a person gives access to all machines, and only an owner names a critical machine; narrowing an agent stays allowed. An empty machines list names none: the agent reaches no machine. Recorded and echoed to the alert chat."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in updateAgentIn) (*mcp.CallToolResult, bridge.AgentView, error) {
			p, err := caller(ctx)
			if err != nil {
				return nil, bridge.AgentView{}, err
			}
			who := bridge.ActorOf(p, "mcp")
			machines, err := bridge.MachineChoice(in.MachineAccess, in.Machines)
			if err != nil {
				return nil, bridge.AgentView{}, err
			}
			v, err := b.UpdateAgent(ctx, who, in.Name, bridge.AgentUpdate{Preset: in.Preset, Scopes: in.Scopes, Machines: machines})
			return nil, v, err
		})
}
