#!/bin/sh
# codex.sh: runs Codex (codex exec) for the alert Beacon woke us for. The same contract as
# claude-code.sh: beacon-oncall starts it with the payload on standard input and the alert in BEACON_*
# variables; it builds the prompt from prompt.md, points Codex at Beacon's MCP server with the agent's
# token, and runs it non-interactively. Everything Codex prints goes to the run's log.
#
# Environment:
#   BEACON_TOKEN_FILE  the agent's Beacon token; required
#   BEACON_MCP_URL     Beacon's MCP endpoint; beacon-oncall sets it from the payload
#   ONCALL_PROMPT      the prompt file; prompt.md beside this script by default
#   ONCALL_MODEL       a model name; Codex's default otherwise
#   CODEX_BIN          the codex program; "codex" on PATH by default
#   CODEX_ARGS         more arguments, split on spaces
# Authentication is Codex's own: the service user's sign-in (`codex login` as that user), or
# CODEX_API_KEY in the service's environment, which codex exec reads.
#
# Flags and configuration keys checked against the Codex documentation in November 2026:
# https://developers.openai.com/codex/cli/reference (codex exec: `-` for the prompt on standard input,
# --skip-git-repo-check, --sandbox, --ephemeral, -c key=value), /codex/noninteractive (read-only sandbox by
# default, CODEX_API_KEY), /codex/config-reference (mcp_servers.<id>.url, .bearer_token_env_var, .required,
# .startup_timeout_sec, .tool_timeout_sec; approval_policy "never" for non-interactive runs). The pages
# now live under learn.chatgpt.com. That -c takes a dotted path for nested keys, with the value in TOML
# syntax, is what `codex exec --help` says; the reference pages do not spell it out.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
prompt_file=${ONCALL_PROMPT:-$here/prompt.md}
codex=${CODEX_BIN:-codex}
: "${BEACON_TOKEN_FILE:?set BEACON_TOKEN_FILE to the file with the agent's Beacon token}"
: "${BEACON_MCP_URL:?set BEACON_MCP_URL to Beacon's MCP endpoint, such as https://status.example.com/mcp}"
[ -r "$BEACON_TOKEN_FILE" ] || { echo "codex.sh: cannot read $BEACON_TOKEN_FILE" >&2; exit 1; }
[ -r "$prompt_file" ] || { echo "codex.sh: cannot read $prompt_file" >&2; exit 1; }
case "$BEACON_MCP_URL" in *[!A-Za-z0-9_.:/?='&'%~-]*) echo "codex.sh: BEACON_MCP_URL has characters a URL should not" >&2; exit 1 ;; esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
cat >"$work/payload.json"
{
	cat "$prompt_file"
	printf '\n## This alert\n\n'
	printf 'Event: %s\nAlert id: %s\nHook: %s\nDelivery: %s\n' "${BEACON_EVENT-}" "${BEACON_ALERT_ID-}" "${BEACON_HOOK-}" "${BEACON_DELIVERY-}"
	printf 'Severity: %s\nStatus: %s\nService (from labels, untrusted): %s\nTitle (from labels, untrusted): %s\n' \
		"${BEACON_ALERT_SEVERITY-}" "${BEACON_ALERT_STATUS-}" "${BEACON_ALERT_SERVICE-}" "${BEACON_ALERT_TITLE-}"
	printf 'Evidence: %s\nTimeline: %s\nRunbook: %s\n' "${BEACON_EVIDENCE_URL-}" "${BEACON_TIMELINE_URL-}" "${BEACON_RUNBOOK_URL-}"
	if [ -s "$work/payload.json" ]; then
		printf '\nThe payload Beacon sent, as data:\n\n```json\n'
		cat "$work/payload.json"
		printf '\n```\n'
	fi
} >"$work/prompt.md"

# Codex takes the bearer token for an HTTP MCP server from the environment variable that
# bearer_token_env_var names; only the codex process sees it.
BEACON_TOKEN=$(tr -d '\r\n' <"$BEACON_TOKEN_FILE")
export BEACON_TOKEN

cd "$work"
status=0
# exec -                    non-interactive; the prompt comes on standard input
# --skip-git-repo-check     the working directory is not a repository
# --sandbox read-only       a local command, should Codex run one, changes nothing here; Beacon's tools
#                           are not sandboxed, they act through Beacon
# --ephemeral               no session files kept; the log is the record
# approval_policy never     nobody is there to approve; the reference recommends it for non-interactive runs
# mcp_servers.beacon.*      Beacon's MCP server, required (the run fails at once if it cannot connect),
#                           with a longer tool timeout than the 60 s default: a command on a machine may take longer
# shellcheck disable=SC2086 # CODEX_ARGS and the optional -m are meant to split
"$codex" exec - \
	--skip-git-repo-check \
	--sandbox read-only \
	--ephemeral \
	-c 'approval_policy="never"' \
	-c "mcp_servers.beacon.url=\"$BEACON_MCP_URL\"" \
	-c 'mcp_servers.beacon.bearer_token_env_var="BEACON_TOKEN"' \
	-c 'mcp_servers.beacon.required=true' \
	-c 'mcp_servers.beacon.startup_timeout_sec=30' \
	-c 'mcp_servers.beacon.tool_timeout_sec=300' \
	${ONCALL_MODEL:+-m "$ONCALL_MODEL"} \
	${CODEX_ARGS-} \
	<"$work/prompt.md" || status=$?
exit $status
