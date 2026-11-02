#!/bin/sh
# claude-code.sh: runs Claude Code in headless mode for the alert Beacon woke us for.
#
# linkos-oncall starts this with Beacon's payload on standard input and the alert in BEACON_* variables
# (docs/agents.md, "Running an agent when woken"). The script builds the prompt from prompt.md and the
# alert, points Claude Code at Beacon's MCP server with the agent's own token, lets it use Beacon's tools
# and nothing else, and runs it with nobody to ask. Everything Claude Code prints goes to the run's log.
#
# Environment:
#   BEACON_TOKEN_FILE  the agent's Beacon token (scopes status:read and alerts:write; machines:ssh to work
#                      on machines); required
#   BEACON_MCP_URL     Beacon's MCP endpoint; linkos-oncall sets it from the payload (links.mcp)
#   ONCALL_PROMPT      the prompt file; prompt.md beside this script by default
#   ONCALL_MAX_TURNS   agentic turns Claude Code may take; 60 by default
#   ONCALL_MODEL       a model alias or name; Claude Code's default otherwise
#   CLAUDE_BIN         the claude program; "claude" on PATH by default
#   CLAUDE_ARGS        more arguments, split on spaces, such as "--bare" (then authenticate with
#                      ANTHROPIC_API_KEY: bare mode does not read a sign-in or CLAUDE_CODE_OAUTH_TOKEN)
# Authentication is Claude Code's own: the service user's sign-in in $HOME (run `claude` once as that
# user), or ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN in the service's environment.
#
# Every flag and configuration field used here was checked against the Claude Code documentation in
# November 2026: https://code.claude.com/docs/en/cli-reference (flags), /docs/en/mcp (the MCP
# configuration file, headersHelper, environment variables), /docs/en/permissions (mcp__<server>__*
# rules), /docs/en/headless (the prompt on standard input, dontAsk).
set -eu

case "${1-}" in
headers)
	# Claude Code runs this as the MCP server's headersHelper when it connects, and again after a 401 or
	# 403, so the token is read from the file each time and renews with it. The helper must print a JSON
	# object of header names to values. The file comes as an argument, whatever the environment.
	token=$(tr -d '\r\n' <"$2")
	printf '{"Authorization": "Bearer %s"}\n' "$token"
	exit 0
	;;
esac

here=$(cd "$(dirname "$0")" && pwd)
self="$here/$(basename "$0")"
prompt_file=${ONCALL_PROMPT:-$here/prompt.md}
max_turns=${ONCALL_MAX_TURNS:-60}
claude=${CLAUDE_BIN:-claude}
: "${BEACON_TOKEN_FILE:?set BEACON_TOKEN_FILE to the file with the agent's Beacon token}"
: "${BEACON_MCP_URL:?set BEACON_MCP_URL to Beacon's MCP endpoint, such as https://status.example.com/mcp}"
[ -r "$BEACON_TOKEN_FILE" ] || { echo "claude-code.sh: cannot read $BEACON_TOKEN_FILE" >&2; exit 1; }
[ -r "$prompt_file" ] || { echo "claude-code.sh: cannot read $prompt_file" >&2; exit 1; }
# These go into a JSON file and a shell command without quoting, so they must be plain.
case "$self$BEACON_TOKEN_FILE" in *[!A-Za-z0-9_./-]*) echo "claude-code.sh: the script's and the token file's paths must be plain (letters, digits, _ . / -)" >&2; exit 1 ;; esac
case "$BEACON_MCP_URL" in *[!A-Za-z0-9_.:/?='&'%~-]*) echo "claude-code.sh: BEACON_MCP_URL has characters a URL should not" >&2; exit 1 ;; esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
cat >"$work/payload.json" # the payload, or nothing when run by hand

# The prompt: the instructions, then the alert as data. Claude Code reads it from standard input, as the
# headless documentation's own examples do (`… | claude -p --output-format json`). Should your version
# insist on a prompt argument, pass "$(cat "$work/prompt.md")" after -p instead.
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

# Beacon's MCP server. "type": "http" is required for a URL entry; headersHelper is a command Claude Code
# runs in a shell when connecting, given as an absolute path; its headers override static ones.
cat >"$work/mcp.json" <<EOF
{"mcpServers": {"beacon": {"type": "http", "url": "$BEACON_MCP_URL", "headersHelper": "$self headers $BEACON_TOKEN_FILE"}}}
EOF

# An empty working directory: no CLAUDE.md, .mcp.json or project settings of anyone's get loaded.
cd "$work"
status=0
# -p                        non-interactive; the prompt comes on standard input
# --output-format stream-json --verbose
#                           one JSON line per message, so the log shows every tool call and its result
# --mcp-config, --strict-mcp-config
#                           Beacon's MCP server, and only MCP servers from this file
# --tools ""                no built-in tools: no local shell, files or web; MCP tools are unaffected
# --allowedTools "mcp__beacon__*"
#                           every tool of the beacon server runs without asking
# --permission-mode dontAsk anything else that would ask is denied; nobody is there to answer
# --max-turns               a bound on the work; the run ends with an error when it is reached
# --no-session-persistence  nothing to resume later; the log is the record
# shellcheck disable=SC2086 # CLAUDE_ARGS and the optional --model are meant to split
"$claude" -p \
	--output-format stream-json --verbose \
	--mcp-config "$work/mcp.json" --strict-mcp-config \
	--tools "" \
	--allowedTools "mcp__beacon__*" \
	--permission-mode dontAsk \
	--max-turns "$max_turns" \
	--no-session-persistence \
	${ONCALL_MODEL:+--model "$ONCALL_MODEL"} \
	${CLAUDE_ARGS-} \
	<"$work/prompt.md" || status=$?
exit $status
