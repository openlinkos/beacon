package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openlinkos/beacon/internal/bridge"
)

// People and the caller's SSH keys, as the REST routes under /api/v1/users and /api/v1/ssh-keys offer
// them: list_users and invite_user for an owner's credential (accounts:write); list_ssh_keys,
// add_ssh_key and remove_ssh_key for any credential with machines:ssh, on its own keys. Password resets
// and role changes are not offered here: they are for people, on the console or the host.

type (
	usersOut struct {
		Users       []bridge.UserView `json:"users"`
		Invitations []bridge.LinkView `json:"invitations"`
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
)

func addPeopleTools(s *mcp.Server, b *bridge.Bridge) {
	mcp.AddTool(s, &mcp.Tool{Name: "list_users", Title: "List people", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)},
		Description: "The people who sign in to Beacon (username, display name, role, last sign-in, whether disabled, their agents) and the invitations not yet used. Needs accounts:write."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, usersOut, error) {
			who, err := need(ctx, "accounts:write")
			if err != nil {
				return nil, usersOut{}, err
			}
			users, invs, err := b.Users(who)
			return nil, usersOut{Users: users, Invitations: invs}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "invite_user", Title: "Invite a person",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: t(false), OpenWorldHint: t(false)},
		Description: "Make a one-time link, valid for 24 hours, with which a person joins Beacon with a role, choosing their username and password. Hand the link to that person only. Needs accounts:write. Recorded and echoed to the alert chat (without the link)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in inviteIn) (*mcp.CallToolResult, bridge.LinkView, error) {
			who, err := need(ctx, "accounts:write")
			if err != nil {
				return nil, bridge.LinkView{}, err
			}
			l, err := b.Invite(ctx, who, in.Role, in.Note)
			return nil, l, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_ssh_keys", Title: "Your SSH keys", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: t(false)},
		Description: "The public keys you sign in to Beacon's SSH channel with, for your own ssh, scp, sftp and rsync."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, keysOut, error) {
			who, err := need(ctx, "machines:ssh")
			if err != nil {
				return nil, keysOut{}, err
			}
			keys, err := b.SSHKeys(who, "")
			return nil, keysOut{Keys: keys}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "add_ssh_key", Title: "Register your SSH key",
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

	mcp.AddTool(s, &mcp.Tool{Name: "remove_ssh_key", Title: "Remove your SSH key",
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
}
