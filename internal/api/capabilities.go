package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// Canonical operational domain identifiers shared across REST (/api/v1/capabilities),
// OpenAPI tags, the MCP server (_meta, resources, prompts) and the operations workspace.
const (
	DomainIncidents  = "incidents"
	DomainMonitoring = "monitoring"
	DomainMachines   = "machines"
	DomainStatus     = "status"
	DomainIdentity   = "identity"
	DomainGovernance = "governance"
)

// Features describes which optional Beacon subsystems are enabled on this instance.
type Features struct {
	SSH             bool   `json:"ssh"`
	SSHPaused       bool   `json:"ssh_paused"`
	Config          bool   `json:"config"`
	Status          bool   `json:"status"`
	ProbeNow        bool   `json:"probe_now"`
	TeamCredentials bool   `json:"team_credentials"`
	ShareLinks      bool   `json:"share_links"`
	MCPEndpoint     string `json:"mcp_endpoint,omitempty"`
	OpenAPIURL      string `json:"openapi_url,omitempty"`
	DocsURL         string `json:"docs_url,omitempty"`
}

// CallerCapabilities summarises who the authenticated caller is and what boundaries apply right now.
type CallerCapabilities struct {
	Actor        string   `json:"actor"`
	User         string   `json:"user,omitempty"`
	Agent        string   `json:"agent,omitempty"`
	Preset       string   `json:"preset,omitempty"`
	Scopes       []string `json:"scopes"`
	MachineScope string   `json:"machine_scope"` // all, listed or none; machine_access says the same in today's words
	bridge.MachineAccessView
	Person     bool `json:"person"`
	Standalone bool `json:"standalone"`
}

// Operation describes one Beacon operation across REST and MCP, evaluated against the caller.
type Operation struct {
	Tool              string   `json:"tool"`
	REST              string   `json:"rest"`
	Domain            string   `json:"domain"`
	Access            string   `json:"access"` // read, write, destructive
	Scopes            []string `json:"scopes"`
	ScopeMode         string   `json:"scope_mode,omitempty"` // any (default), all, standalone_admin, owner_or_self
	Summary           string   `json:"summary"`
	Available         bool     `json:"available"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
}

// Domain groups related operations with a clear workflow guide for people and AI agents.
type Domain struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Tag        string      `json:"tag"`
	Summary    string      `json:"summary"`
	Workflow   string      `json:"workflow"`
	Operations []Operation `json:"operations"`
}

// MCPResourceSpec describes a read-only MCP resource or RFC 6570 resource template.
type MCPResourceSpec struct {
	URI         string   `json:"uri"`
	Template    bool     `json:"template,omitempty"`
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Domain      string   `json:"domain"`
	Scopes      []string `json:"scopes"`
	Description string   `json:"description"`
	Available   bool     `json:"available"`
}

// MCPPromptArg describes one argument of an operational MCP prompt.
type MCPPromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// MCPPromptSpec describes a reusable operational playbook exposed through MCP prompts.
type MCPPromptSpec struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Domain      string         `json:"domain"`
	Description string         `json:"description"`
	Arguments   []MCPPromptArg `json:"arguments,omitempty"`
}

// Capabilities is the live discovery payload returned by GET /api/v1/capabilities,
// the MCP describe_capabilities tool, beacon://capabilities and the console's Agent & MCP Hub.
type Capabilities struct {
	Caller    CallerCapabilities `json:"caller"`
	Features  Features           `json:"features"`
	Domains   []Domain           `json:"domains"`
	Resources []MCPResourceSpec  `json:"resources"`
	Prompts   []MCPPromptSpec    `json:"prompts"`
}

type opDef struct {
	tool      string
	rest      string
	domain    string
	access    string
	scopes    []string
	scopeMode string
	feature   string
	summary   string
}

type domainDef struct {
	id       string
	title    string
	tag      string
	summary  string
	workflow string
}

var domainDefs = []domainDef{
	{
		id:      DomainIncidents,
		title:   "Alerts & Incident Response",
		tag:     "Incidents",
		summary: "Live alert groups, multi-location failure evidence, timelines, notes, acknowledgements, silences, change events and retained incident history.",
		workflow: "Start with status_summary or list_alerts, call get_evidence and get_timeline for full context in one step, " +
			"record findings with add_note, coordinate ownership with ack_alert or silence_alert, and inspect history via list_incident_episodes / list_incident_outcomes.",
	},
	{
		id:      DomainMonitoring,
		title:   "Monitoring, Probing & Configuration",
		tag:     "Monitoring",
		summary: "On-demand probing across locations plus versioned configuration for locations, targets, modules, heartbeats, expiring resources and status page definitions.",
		workflow: "Inspect current definitions with get_config (capturing version for if_version), verify service fixes immediately with probe_now, " +
			"assign response machines with set_target_machine, and audit or undo changes with list_config_changes / revert_config_change.",
	},
	{
		id:      DomainMachines,
		title:   "Machines & SSH Channel",
		tag:     "Machines & SSH",
		summary: "Enrolled hosts (direct or behind NAT), SSH readiness checks, command execution, file search/read/write/edit, recorded sessions and the global SSH pause switch.",
		workflow: "Check channel posture with get_ssh_status and list_machines, verify connectivity via check_machine_ssh, inspect or edit files with search_files / read_file / edit_file / write_file " +
			"and execute commands via run_command, then audit replays and transcripts through list_sessions / get_session.",
	},
	{
		id:      DomainStatus,
		title:   "Status Pages & Maintenance",
		tag:     "Status & Maintenance",
		summary: "Public status pages with 90-day component history and scheduled maintenance windows that announce publicly and silence affected alerts.",
		workflow: "Read live public state with get_status_page, schedule planned work ahead of time with schedule_maintenance, end or cancel windows via end_maintenance, " +
			"and prefix alert notes with 'public:' to publish incident updates.",
	},
	{
		id:      DomainIdentity,
		title:   "Identity, Agents & Delegation",
		tag:     "Identity & Agents",
		summary: "Self-introspection, capability discovery, agent delegation presets and machine boundaries, SSH keys, token lifecycle and team accounts.",
		workflow: "Call whoami and describe_capabilities to inspect active scopes and machine access. Register your public key via add_ssh_key for native ssh/scp/rsync, " +
			"rotate static tokens with renew_token, or manage agents and invitations within your role.",
	},
	{
		id:      DomainGovernance,
		title:   "Audit, Sharing & Team Credentials",
		tag:     "Governance",
		summary: "Immutable hash-chained audit trail, revocable view-only share links for alerts and sessions, and owner-delegated service credentials.",
		workflow: "Query recorded writes with list_audit, create or revoke scoped view-only share links with create_share_link / revoke_share_links, " +
			"and invoke authorised team service endpoints via list_team_credentials / use_team_credential without exposing secrets.",
	},
}

var opDefs = []opDef{
	// Incidents
	{tool: "status_summary", rest: "GET /api/v1/bridge", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "Alerting pipeline health, human notification routes, firing counts by severity and Feishu budget."},
	{tool: "list_alerts", rest: "GET /api/v1/alerts", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "List firing alert groups (or all groups including resolved in the last 7 days)."},
	{tool: "get_alert", rest: "GET /api/v1/alerts/{id}", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "Get one alert group's labels, acknowledgement, silence, notes, agent calls and signed page link."},
	{tool: "get_evidence", rest: "GET /api/v1/alerts/{id}/evidence", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "Gather full alert evidence in one call: per-location probe failures, service machines, related alerts, changes and sessions."},
	{tool: "get_timeline", rest: "GET /api/v1/alerts/{id}/timeline", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "Chronological incident timeline combining pre-alert changes, sessions, notifications, notes and checks."},
	{tool: "list_incident_episodes", rest: "GET /api/v1/incident-episodes", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "List open and resolved alert episodes by start time within a window up to 31 days."},
	{tool: "list_incident_outcomes", rest: "GET /api/v1/incident-outcomes", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "List retained resolved incident summaries filterable by severity, recovery time, origin and evidence."},
	{tool: "get_incident_outcome", rest: "GET /api/v1/incident-outcomes/{id}", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "Inspect one resolved episode's participation, hook deliveries, programme reports and passing checks."},
	{tool: "list_changes", rest: "GET /api/v1/changes", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "List reported deployment, configuration and Flux change events."},
	{tool: "list_silences", rest: "GET /api/v1/silences", domain: DomainIncidents, access: "read", scopes: []string{"status:read"}, summary: "List active and pending alert silences."},
	{tool: "ack_alert", rest: "POST /api/v1/alerts/{id}/ack", domain: DomainIncidents, access: "write", scopes: []string{"alerts:write"}, summary: "Acknowledge a firing alert group with an optional handover note."},
	{tool: "add_note", rest: "POST /api/v1/alerts/{id}/notes", domain: DomainIncidents, access: "write", scopes: []string{"alerts:write"}, summary: "Leave a note on an alert group (prefix with 'public:' for public status page updates)."},
	{tool: "silence_alert", rest: "POST /api/v1/silences", domain: DomainIncidents, access: "write", scopes: []string{"alerts:write"}, summary: "Silence a firing alert group for up to 24 hours with a required reason."},
	{tool: "expire_silence", rest: "DELETE /api/v1/silences/{id}", domain: DomainIncidents, access: "destructive", scopes: []string{"alerts:write"}, summary: "End an active silence immediately so alerts notify again."},
	{tool: "check_drill_hook", rest: "GET /api/v1/agent-hooks/{name}/first-drill", domain: DomainIncidents, access: "read", scopes: []string{bridge.AgentRunScope}, summary: "Verify a named first-drill hook's signing secret, selector filters and critical chat route."},
	{tool: "report_agent_run", rest: "PUT /api/v1/alerts/{id}/agent-runs/{delivery}", domain: DomainIncidents, access: "write", scopes: []string{bridge.AgentRunScope}, summary: "Report observed execution state and exit code for an assigned on-call hook delivery."},

	// Monitoring & Configuration
	{tool: "probe_now", rest: "POST /api/v1/probe-now", domain: DomainMonitoring, access: "write", scopes: []string{"alerts:write"}, feature: "probe_now", summary: "Run immediate multi-location checks for an alert episode, target or service and return per-location results."},
	{tool: "get_config", rest: "GET /api/v1/config", domain: DomainMonitoring, access: "read", scopes: []string{"status:read", "config:write"}, feature: "config", summary: "Read current configuration (locations, heartbeats, resources, targets, modules, status pages) and version."},
	{tool: "put_config", rest: "PUT /api/v1/config/{kind}/{key}", domain: DomainMonitoring, access: "destructive", scopes: []string{"config:write"}, feature: "config", summary: "Validate and atomically create or update one configuration item using optimistic if_version locking."},
	{tool: "set_target_machine", rest: "PUT /api/v1/config/targets/{name}/groups/{group}/machine", domain: DomainMonitoring, access: "write", scopes: []string{"config:write", "machines:write"}, scopeMode: "all", feature: "config", summary: "Assign or clear the response machine linked to an HTTP target group."},
	{tool: "delete_config", rest: "DELETE /api/v1/config/{kind}/{key}", domain: DomainMonitoring, access: "destructive", scopes: []string{"config:write"}, feature: "config", summary: "Validate and remove one configuration item."},
	{tool: "list_config_changes", rest: "GET /api/v1/config/history", domain: DomainMonitoring, access: "read", scopes: []string{"status:read", "config:write"}, feature: "config", summary: "List versioned configuration changes with actor, timestamp and touched items."},
	{tool: "get_config_change", rest: "GET /api/v1/config/history/{version}", domain: DomainMonitoring, access: "read", scopes: []string{"status:read", "config:write"}, feature: "config", summary: "Inspect one configuration version's exact before and after values."},
	{tool: "revert_config_change", rest: "POST /api/v1/config/history/{version}/revert", domain: DomainMonitoring, access: "destructive", scopes: []string{"config:write"}, feature: "config", summary: "Revert a configuration change as a new audited version."},

	// Machines & SSH Channel
	{tool: "get_ssh_status", rest: "GET /api/v1/ssh", domain: DomainMachines, access: "read", scopes: []string{"status:read", "machines:ssh"}, summary: "Check whether the SSH channel is enabled, paused, its address, host key and certificate authority."},
	{tool: "list_machines", rest: "GET /api/v1/machines", domain: DomainMachines, access: "read", scopes: []string{"status:read", "machines:ssh"}, feature: "ssh", summary: "List enrolled machines, reachability, NAT connector status and ready-to-use ssh_config block."},
	{tool: "check_machine_ssh", rest: "POST /api/v1/machines/{name}/check", domain: DomainMachines, access: "read", scopes: []string{"machines:ssh"}, feature: "ssh", summary: "Verify route, pinned host key and certificate login for a machine without executing a command."},
	{tool: "run_command", rest: "POST /api/v1/machines/{name}/run", domain: DomainMachines, access: "destructive", scopes: []string{"machines:ssh"}, feature: "ssh", summary: "Execute a shell command on an enrolled machine with structured stdout/stderr and recorded session evidence."},
	{tool: "search_files", rest: "POST /api/v1/machines/{name}/search", domain: DomainMachines, access: "read", scopes: []string{"machines:ssh"}, feature: "ssh", summary: "Search literal text inside regular text files under an explicit absolute directory on a machine."},
	{tool: "read_file", rest: "GET /api/v1/machines/{name}/file", domain: DomainMachines, access: "read", scopes: []string{"machines:ssh"}, feature: "ssh", summary: "Read numbered lines from a text file on a machine with offset and limit pagination."},
	{tool: "write_file", rest: "PUT /api/v1/machines/{name}/file", domain: DomainMachines, access: "destructive", scopes: []string{"machines:ssh"}, feature: "ssh", summary: "Write an entire text file (up to 1 MiB) on a machine, preserving existing owner and mode."},
	{tool: "edit_file", rest: "PATCH /api/v1/machines/{name}/file", domain: DomainMachines, access: "destructive", scopes: []string{"machines:ssh"}, feature: "ssh", summary: "Apply an exact string replacement to a file with optimistic conflict detection."},
	{tool: "list_sessions", rest: "GET /api/v1/sessions", domain: DomainMachines, access: "read", scopes: []string{"status:read", "machines:ssh"}, feature: "ssh", summary: "List recorded SSH and tool sessions within your machine visibility scope."},
	{tool: "get_session", rest: "GET /api/v1/sessions/{id}", domain: DomainMachines, access: "read", scopes: []string{"sessions:read", "machines:ssh"}, feature: "ssh", summary: "Retrieve retained session transcript, indexed commands and recording availability."},
	{tool: "add_machine", rest: "POST /api/v1/machines", domain: DomainMachines, access: "write", scopes: []string{"machines:write"}, feature: "ssh", summary: "Enrol a direct or NAT connector machine, pin its host key and return CA setup instructions."},
	{tool: "update_machine", rest: "PATCH /api/v1/machines/{name}", domain: DomainMachines, access: "write", scopes: []string{"machines:write"}, feature: "ssh", summary: "Update a machine's OS user, critical flag or note with optional if_version check."},
	{tool: "machine_history", rest: "GET /api/v1/machines/{name}/history", domain: DomainMachines, access: "read", scopes: []string{"status:read", "machines:ssh"}, feature: "ssh", summary: "List versioned metadata history for a machine's current enrolment."},
	{tool: "restore_machine", rest: "POST /api/v1/machines/{name}/restore", domain: DomainMachines, access: "destructive", scopes: []string{"machines:write"}, feature: "ssh", summary: "Restore a machine's user, critical flag and note from an earlier metadata version."},
	{tool: "set_machine_connector", rest: "PUT /api/v1/machines/{name}/connector", domain: DomainMachines, access: "write", scopes: []string{"machines:write"}, feature: "ssh", summary: "Set or rotate the outbound SSH public key for a NAT connector machine."},
	{tool: "remove_machine", rest: "DELETE /api/v1/machines/{name}", domain: DomainMachines, access: "destructive", scopes: []string{"machines:write"}, feature: "ssh", summary: "Unenrol a machine while keeping its recorded session history."},
	{tool: "pause_ssh", rest: "POST /api/v1/ssh/pause", domain: DomainMachines, access: "destructive", scopes: []string{"machines:write"}, feature: "ssh", summary: "Emergency brake: immediately close open agent SSH sessions and refuse new ones."},
	{tool: "resume_ssh", rest: "POST /api/v1/ssh/resume", domain: DomainMachines, access: "write", scopes: []string{"machines:write"}, feature: "ssh", summary: "Re-enable agent SSH access after pause_ssh."},

	// Status Pages & Maintenance
	{tool: "get_status_page", rest: "GET /api/v1/status/{name}", domain: DomainStatus, access: "read", scopes: []string{"status:read"}, feature: "status", summary: "Fetch a public status page's current state, 90-day component history, incidents and maintenance."},
	{tool: "list_maintenance", rest: "GET /api/v1/maintenance", domain: DomainStatus, access: "read", scopes: []string{"status:read"}, summary: "List planned, ongoing and recently completed maintenance windows."},
	{tool: "schedule_maintenance", rest: "POST /api/v1/maintenance", domain: DomainStatus, access: "write", scopes: []string{"maintenance:write"}, summary: "Schedule a public maintenance window and automatically silence affected services."},
	{tool: "end_maintenance", rest: "DELETE /api/v1/maintenance/{id}", domain: DomainStatus, access: "write", scopes: []string{"maintenance:write"}, summary: "End an ongoing maintenance window early or cancel an upcoming one."},

	// Identity, Agents & Delegation
	{tool: "describe_capabilities", rest: "GET /api/v1/capabilities", domain: DomainIdentity, access: "read", summary: "Discover Beacon's operational domains, enabled features, MCP resources/prompts and caller-available operations."},
	{tool: "whoami", rest: "GET /api/v1/token", domain: DomainIdentity, access: "read", summary: "Inspect current actor identity, owner, preset, effective scopes, machine boundaries and credential expiry."},
	{tool: "renew_token", rest: "POST /api/v1/token/renew", domain: DomainIdentity, access: "write", summary: "Rotate your static token for a new secret with a 10-minute grace window (at most once per day)."},
	{tool: "revoke_token", rest: "DELETE /api/v1/tokens/{name}", domain: DomainIdentity, access: "destructive", scopes: []string{"machines:write"}, summary: "Delete an agent's or an SSH-capable token within your own access, and close its active sessions."},
	{tool: "list_agents", rest: "GET /api/v1/agents", domain: DomainIdentity, access: "read", scopeMode: "owner_or_self", summary: "List delegated agents you own (or all agents for an administrator), with presets, scopes and machines."},
	{tool: "get_agent", rest: "GET /api/v1/agents/{name}", domain: DomainIdentity, access: "read", scopeMode: "owner_or_self", summary: "Inspect one delegated agent's effective scopes, machines, tokens, OAuth grants and SSH keys."},
	{tool: "create_agent", rest: "POST /api/v1/agents", domain: DomainIdentity, access: "write", scopeMode: "owner_or_self", summary: "Create a delegated agent bounded by owner role and caller privileges, optionally issuing a token."},
	{tool: "update_agent", rest: "PATCH /api/v1/agents/{name}", domain: DomainIdentity, access: "write", scopeMode: "owner_or_self", summary: "Update an agent's preset, custom scopes or machine selection within permitted bounds."},
	{tool: "list_ssh_keys", rest: "GET /api/v1/ssh-keys", domain: DomainIdentity, access: "read", scopes: []string{"machines:ssh"}, summary: "List your registered public SSH keys for Beacon's SSH channel."},
	{tool: "add_ssh_key", rest: "POST /api/v1/ssh-keys", domain: DomainIdentity, access: "write", scopes: []string{"machines:ssh"}, summary: "Register a public SSH key so native ssh, scp, sftp and rsync work through Beacon."},
	{tool: "remove_ssh_key", rest: "DELETE /api/v1/ssh-keys", domain: DomainIdentity, access: "destructive", scopes: []string{"machines:ssh"}, summary: "Remove one of your SSH public keys by fingerprint and close matching sessions."},
	{tool: "list_users", rest: "GET /api/v1/users", domain: DomainIdentity, access: "read", scopes: []string{"accounts:write"}, scopeMode: "standalone_admin", summary: "List Beacon accounts, pending invitations and pending password reset links (standalone owner credential required)."},
	{tool: "invite_user", rest: "POST /api/v1/users/invitations", domain: DomainIdentity, access: "write", scopes: []string{"accounts:write"}, scopeMode: "standalone_admin", summary: "Generate a one-time 24-hour invitation link for a new team member, within the roles your scopes cover."},
	{tool: "withdraw_link", rest: "DELETE /api/v1/users/links/{id}", domain: DomainIdentity, access: "destructive", scopes: []string{"accounts:write"}, scopeMode: "standalone_admin", summary: "Withdraw a pending invitation or password reset link so it stops working at once."},

	// Governance: Audit, Share Links & Team Credentials
	{tool: "list_audit", rest: "GET /api/v1/audit", domain: DomainGovernance, access: "read", scopes: []string{"status:read"}, summary: "Query the hash-chained audit log of recorded writes, filterable by actor, action and target."},
	{tool: "create_share_link", rest: "POST /api/v1/share-links/{kind}/{id}", domain: DomainGovernance, access: "write", scopes: []string{bridge.ShareScope}, scopeMode: "standalone_admin", feature: "share_links", summary: "Issue a revocable time-limited view-only link for an alert episode or SSH session."},
	{tool: "revoke_share_links", rest: "POST /api/v1/share-links/{kind}/{id}/revoke", domain: DomainGovernance, access: "destructive", scopes: []string{bridge.ShareScope}, scopeMode: "standalone_admin", feature: "share_links", summary: "Revoke all outstanding share links for an alert group or SSH session."},
	{tool: "list_team_credentials", rest: "GET /api/v1/team-credentials", domain: DomainGovernance, access: "read", scopes: []string{"secrets:use"}, feature: "team_credentials", summary: "List metadata for team service credentials granted to the caller."},
	{tool: "use_team_credential", rest: "POST /api/v1/team-credentials/{id}/use", domain: DomainGovernance, access: "write", scopes: []string{"secrets:use"}, feature: "team_credentials", summary: "Execute the owner-configured HTTPS request for a granted team credential without exposing its secret."},
}

var opIndex = func() map[string]opDef {
	m := make(map[string]opDef, len(opDefs))
	for _, op := range opDefs {
		m[op.tool] = op
	}
	return m
}()

// ToolMeta returns structured MCP _meta attributes for a registered tool name.
func ToolMeta(name string) map[string]any {
	op, ok := opIndex[name]
	if !ok {
		return nil
	}
	meta := map[string]any{
		"domain": op.domain,
		"access": op.access,
		"rest":   op.rest,
	}
	if len(op.scopes) > 0 {
		meta["scopes"] = slices.Clone(op.scopes)
	}
	return meta
}

// MCPPrompts returns the canonical catalog of operational MCP prompts offered by Beacon.
func MCPPrompts() []MCPPromptSpec {
	return []MCPPromptSpec{
		{
			Name:        "triage_alert",
			Title:       "Triage a firing alert",
			Domain:      DomainIncidents,
			Description: "Investigate a firing alert end to end: collect evidence and timeline, correlate recent changes, inspect response machines read-only, and post a structured finding note.",
			Arguments: []MCPPromptArg{
				{Name: "alert_id", Description: "Alert group ID from list_alerts or the wake payload", Required: true},
			},
		},
		{
			Name:        "verify_recovery",
			Title:       "Verify service recovery",
			Domain:      DomainMonitoring,
			Description: "Trigger multi-location probe_now verification bound to an incident episode, evaluate probe consensus and leave a clear handover note.",
			Arguments: []MCPPromptArg{
				{Name: "alert_id", Description: "Alert group ID to verify", Required: true},
				{Name: "expected_episode", Description: "Incident episode ID from alert.episode (optional but recommended)", Required: false},
			},
		},
		{
			Name:        "fleet_health",
			Title:       "Fleet health review",
			Domain:      DomainMachines,
			Description: "Produce a concise operational health report across alerts, probe locations, enrolled machines, silences, maintenance windows and recent changes.",
		},
		{
			Name:        "shift_handover",
			Title:       "Prepare shift handover",
			Domain:      DomainIncidents,
			Description: "Summarise active incidents, recent episodes, machine sessions, changes and upcoming maintenance for the incoming on-call engineer.",
			Arguments: []MCPPromptArg{
				{Name: "hours", Description: "Lookback window in hours (default 8)", Required: false},
			},
		},
		{
			Name:        "onboard_monitor",
			Title:       "Onboard a service monitor",
			Domain:      DomainMonitoring,
			Description: "Safely add or update an HTTP service monitor in Beacon's versioned configuration using if_version and verify immediately with probe_now.",
			Arguments: []MCPPromptArg{
				{Name: "service", Description: "Logical service identifier (e.g. api.example.com)", Required: true},
				{Name: "url", Description: "Target URL to probe (e.g. https://api.example.com/healthz)", Required: true},
			},
		},
	}
}

func featureEnabled(f Features, name string) (bool, string) {
	switch name {
	case "":
		return true, ""
	case "ssh":
		if !f.SSH {
			return false, "the SSH channel is disabled on this Beacon instance"
		}
		return true, ""
	case "config":
		if !f.Config {
			return false, "database-managed configuration is not enabled on this instance"
		}
		return true, ""
	case "status":
		if !f.Status {
			return false, "public status pages are not enabled on this instance"
		}
		return true, ""
	case "probe_now":
		if !f.ProbeNow {
			return false, "on-demand probing requires the probe gateway on this instance"
		}
		return true, ""
	case "team_credentials":
		if !f.TeamCredentials {
			return false, "the team credential store is not enabled on this instance"
		}
		return true, ""
	case "share_links":
		if !f.ShareLinks {
			return false, "signed share links are not configured on this instance"
		}
		return true, ""
	default:
		return true, ""
	}
}

func evaluateOp(p store.Principal, f Features, op opDef) (bool, string) {
	if ok, reason := featureEnabled(f, op.feature); !ok {
		return false, reason
	}
	if op.tool == "run_command" || op.tool == "search_files" || op.tool == "read_file" || op.tool == "write_file" || op.tool == "edit_file" || op.tool == "check_machine_ssh" {
		if f.SSHPaused && p.Agent != "" {
			return false, "agent SSH access is currently paused"
		}
	}
	switch op.scopeMode {
	case "standalone_admin":
		if p.Agent != "" {
			return false, "requires a person account or standalone administration credential, not a delegated agent"
		}
		if len(op.scopes) > 0 && !slices.ContainsFunc(op.scopes, p.Has) {
			return false, "requires scope " + strings.Join(op.scopes, " or ")
		}
		return true, ""
	case "owner_or_self":
		if p.Agent != "" {
			return false, "delegated agents cannot manage agent identities"
		}
		if !p.Person() && !p.Has("accounts:write") {
			return false, "requires a person account or a standalone credential with accounts:write"
		}
		return true, ""
	case "all":
		var missing []string
		for _, sc := range op.scopes {
			if !p.Has(sc) {
				missing = append(missing, sc)
			}
		}
		if len(missing) > 0 {
			return false, "requires scopes: " + strings.Join(missing, ", ")
		}
		return true, ""
	default:
		if len(op.scopes) > 0 && !slices.ContainsFunc(op.scopes, p.Has) {
			return false, "requires scope " + strings.Join(op.scopes, " or ")
		}
		return true, ""
	}
}

// BuildCapabilities constructs the full caller-aware domain, operation, resource and prompt matrix.
func BuildCapabilities(p store.Principal, f Features) Capabilities {
	if f.OpenAPIURL == "" {
		f.OpenAPIURL = "/api/v1/openapi.yaml"
	}
	if f.MCPEndpoint == "" {
		f.MCPEndpoint = "/mcp"
	}
	if f.DocsURL == "" {
		f.DocsURL = DocsURL
	}
	scopes := slices.Clone(p.Scopes)
	if scopes == nil {
		scopes = []string{}
	}
	caller := CallerCapabilities{
		Actor:             p.Name(),
		User:              p.User,
		Agent:             p.Agent,
		Preset:            p.Preset,
		Scopes:            scopes,
		MachineScope:      bridge.MachineScope(p.MachineAccess),
		MachineAccessView: bridge.AccessView(p.MachineAccess),
		Person:            p.Person(),
		Standalone:        p.Agent == "" && !p.Person(),
	}

	byDomain := make(map[string][]Operation, len(domainDefs))
	for _, op := range opDefs {
		avail, reason := evaluateOp(p, f, op)
		sc := slices.Clone(op.scopes)
		if sc == nil {
			sc = []string{}
		}
		byDomain[op.domain] = append(byDomain[op.domain], Operation{
			Tool:              op.tool,
			REST:              op.rest,
			Domain:            op.domain,
			Access:            op.access,
			Scopes:            sc,
			ScopeMode:         op.scopeMode,
			Summary:           op.summary,
			Available:         avail,
			UnavailableReason: reason,
		})
	}

	domains := make([]Domain, 0, len(domainDefs))
	for _, d := range domainDefs {
		domains = append(domains, Domain{
			ID:         d.id,
			Title:      d.title,
			Tag:        d.tag,
			Summary:    d.summary,
			Workflow:   d.workflow,
			Operations: byDomain[d.id],
		})
	}

	resources := []MCPResourceSpec{
		{
			URI: "beacon://capabilities", Name: "capabilities", Title: "Beacon capabilities & domain catalog",
			Domain: DomainIdentity, Scopes: []string{}, Available: true,
			Description: "Structured operational domains, enabled features and caller-scoped tool availability.",
		},
		{
			URI: "beacon://overview", Name: "overview", Title: "Operational health overview",
			Domain: DomainIncidents, Scopes: []string{"status:read"}, Available: p.Has("status:read"),
			Description: "Live alerting summary, notification routes and SSH channel posture.",
		},
		{
			URI: "beacon://alerts/firing", Name: "firing_alerts", Title: "Currently firing alerts",
			Domain: DomainIncidents, Scopes: []string{"status:read"}, Available: p.Has("status:read"),
			Description: "Active firing alert groups ordered by severity and start time.",
		},
		{
			URI: "beacon://machines", Name: "machines", Title: "Enrolled machines & SSH config",
			Domain: DomainMachines, Scopes: []string{"status:read", "machines:ssh"}, Available: f.SSH && (p.Has("status:read") || p.Has("machines:ssh")),
			Description: "Enrolled machines, NAT connector status and ready-to-use ssh_config block.",
		},
		{
			URI: "beacon://silences", Name: "silences", Title: "Active alert silences",
			Domain: DomainIncidents, Scopes: []string{"status:read"}, Available: p.Has("status:read"),
			Description: "Currently active and pending alert silences.",
		},
		{
			URI: "beacon://maintenance", Name: "maintenance", Title: "Maintenance windows",
			Domain: DomainStatus, Scopes: []string{"status:read"}, Available: p.Has("status:read"),
			Description: "Scheduled, active and recently ended maintenance windows.",
		},
		{
			URI: "beacon://alerts/{id}/evidence", Template: true, Name: "alert_evidence", Title: "Alert evidence bundle",
			Domain: DomainIncidents, Scopes: []string{"status:read"}, Available: p.Has("status:read"),
			Description: "Full diagnostic evidence for one alert group ID.",
		},
		{
			URI: "beacon://alerts/{id}/timeline", Template: true, Name: "alert_timeline", Title: "Alert timeline",
			Domain: DomainIncidents, Scopes: []string{"status:read"}, Available: p.Has("status:read"),
			Description: "Chronological event timeline for one alert group ID.",
		},
		{
			URI: "beacon://sessions/{id}", Template: true, Name: "session_transcript", Title: "SSH session transcript",
			Domain: DomainMachines, Scopes: []string{"sessions:read", "machines:ssh"}, Available: f.SSH && (p.Has("sessions:read") || p.Has("machines:ssh")),
			Description: "Retained transcript and command index for one SSH session ID.",
		},
	}
	if f.Status {
		resources = append(resources, MCPResourceSpec{
			URI: "beacon://status/{name}", Template: true, Name: "status_page", Title: "Public status page",
			Domain: DomainStatus, Scopes: []string{"status:read"}, Available: p.Has("status:read"),
			Description: "Computed public status page state, 90-day component history and incidents.",
		})
	}
	if f.Config {
		resources = append(resources, MCPResourceSpec{
			URI: "beacon://config/{kind}", Template: true, Name: "config_kind", Title: "Configuration section",
			Domain: DomainMonitoring, Scopes: []string{"status:read", "config:write"}, Available: p.Has("status:read") || p.Has("config:write"),
			Description: "Configuration items for one kind: locations, heartbeats, resources, targets, modules or status_pages.",
		})
	}

	return Capabilities{
		Caller:    caller,
		Features:  f,
		Domains:   domains,
		Resources: resources,
		Prompts:   MCPPrompts(),
	}
}

func init() {
	Routes = append(Routes, "GET /api/v1/capabilities")
}

func (s *Server) registerCapabilities(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/capabilities", s.withPrincipal("", s.capabilities))
}

func (s *Server) features() Features {
	return Features{
		SSH:             s.SSH != nil,
		SSHPaused:       s.SSH != nil && s.SSH.Paused(),
		Config:          s.Config != nil,
		Status:          s.Status != nil,
		ProbeNow:        s.Bridge != nil && s.Bridge.CanProbeNow(),
		TeamCredentials: s.Credentials != nil || s.CredentialsConfigured,
		ShareLinks:      s.Bridge != nil && s.Bridge.AlertPage("probe") != "",
	}
}

func (s *Server) capabilities(w http.ResponseWriter, _ *http.Request, p store.Principal) {
	writeJSON(w, http.StatusOK, BuildCapabilities(p, s.features()))
}
