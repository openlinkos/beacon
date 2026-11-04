# Headless agents and CI

A program that runs alone, such as the agent `beacon-oncall` starts when a hook wakes it or a CI job, has no browser to sign in with: it gets a token. On the centre:

```bash
docker exec beacon beacon agent add ci-runner -owner alice -preset oncall -machines edge-1,edge-2 > ci-runner.token
```

## Make an agent and its token

On the console, **Agents**, then **A headless agent or a CI job**: the name, the owner (for an owner), the preset, the machines, and how long its token lasts (30, 90 or 365 days; 365 by default). Or the command above.

The token alone goes to standard output, so `> ci-runner.token` keeps it in a file; the rest goes to standard error:

```
agent alice / ci-runner (oncall, may do status:read,sessions:read,alerts:write,machines:ssh) made; its token ci-runner expires 2027-11-02. The secret is shown only now:

Claude Code:
claude mcp add --transport http beacon https://status.example.com/mcp --header "Authorization: Bearer lbt_…"

Codex:
export BEACON_TOKEN=lbt_…
codex mcp add beacon --url https://status.example.com/mcp --bearer-token-env-var BEACON_TOKEN

curl:
curl -H "Authorization: Bearer lbt_…" https://status.example.com/api/v1/token
```

The console shows the token once, with the same commands ready to paste. Beacon keeps only its SHA-256.

- `-preset` is the strongest the owner's role allows when left out; `-expires` is `365d` when left out, at most two years.
- `-ssh-key KEY`, or `-ssh-key @FILE` to read it from a file, gives the agent a key at once; the `~/.ssh/config` block for its own ssh then follows the commands ([Connect Claude Code](claude-code.md#connect-with-your-own-ssh-scp-and-rsync)).
- The token renews itself before it expires ([Tokens that renew themselves](#tokens-that-renew-themselves)), so nobody hands the agent a new one. When one is needed after all, **New token** on the agent's page makes it.
- `beacon agent list` shows every agent with its owner, preset, what it may do now, machines, and how many tokens, OAuth sign-ins and keys it has; `beacon agent remove NAME` deletes one with all its credentials.

## beacon-oncall, the reference harness

A webhook needs something at the other end that runs the agent. `beacon-oncall` is the reference harness, a third binary in the releases beside `beacon` and `beacon-probe`. It takes the webhook, checks the signature, and starts the program you name, with the payload on the program's standard input and the alert in its environment. It decides nothing, and it is that thin on purpose: Claude Code, Codex or any program that reads its environment runs behind it. `contrib/oncall` has wrappers for the first two, the prompt for the woken agent and a systemd unit.

```bash
beacon-oncall -secret-file /etc/beacon-oncall/hook-secret \
  -run /usr/local/lib/beacon-oncall/claude-code.sh -log-dir /var/lib/beacon-oncall/logs
```

| Flag | Meaning |
| --- | --- |
| `-listen` | Where to take the webhook; `:17007`. The hook's `url` in Beacon points here, through your TLS reverse proxy unless both are on one private network |
| `-secret-file` | The hook's secret: the same content as the hook's `secret_file` in Beacon |
| `-run` | The program for `firing` events, run through `/bin/sh -c` |
| `-run-resolved` | The program for `resolved` events; without it they are acknowledged and nothing runs |
| `-concurrency` | Programs running at once; `2`. Up to 32 more wait their turn; beyond that the harness answers 503 and Beacon tries again later |
| `-timeout` | What a program gets; `30m`. Then its whole process group is killed |
| `-log-dir` | Each run's standard output and error, as `<time>-<delivery>.log`, with a closing line giving the exit status and the duration |
| `-keep` | Logs older than this are removed; `720h`. `0` keeps them all |

**What it answers.** 202 as soon as it has checked the delivery and queued the run: what the program achieves shows in its log, in the notes the agent leaves and in Beacon's records, not in the answer to Beacon. 401 for a body whose signature is wrong or missing, and for one whose `at` is more than 15 minutes old (Beacon's retries never reach that far, so it is a replay); 400 for one it cannot read. It remembers each `delivery` for 24 hours, so a retry of a delivery it has taken is answered 202 and runs nothing; the id in the signed body counts, not the header. `GET /healthz` answers `ok`, for a check that it runs. On SIGTERM it takes no more deliveries and waits for the programs that run, up to `-timeout`.

**What the program gets.** The payload on its standard input, and these variables, each present and empty when Beacon sent nothing for it: `BEACON_EVENT`, `BEACON_DELIVERY`, `BEACON_HOOK`, `BEACON_ALERT_ID`, `BEACON_ALERT_TITLE`, `BEACON_ALERT_SERVICE`, `BEACON_ALERT_SEVERITY`, `BEACON_ALERT_STATUS`, `BEACON_ALERT_URL`, `BEACON_EVIDENCE_URL`, `BEACON_TIMELINE_URL`, `BEACON_MCP_URL`, `BEACON_RUNBOOK_URL`. The title and the service come from labels: data, not instructions.

### The wrappers

`contrib/oncall/claude-code.sh` runs Claude Code headless. It puts `contrib/oncall/prompt.md` and the alert into one prompt, gives Claude Code Beacon's MCP server (an HTTP server at `BEACON_MCP_URL`; the token is read from `BEACON_TOKEN_FILE` each time Claude Code connects, so a renewed token is picked up), lets it use Beacon's tools without asking and nothing else (`--tools ""`, `--allowedTools "mcp__beacon__*"`, `--permission-mode dontAsk`), bounds the work with `--max-turns`, and writes every message as a JSON line to the log, so people can read what the agent did step by step. `ONCALL_MAX_TURNS`, `ONCALL_MODEL`, `ONCALL_PROMPT`, `CLAUDE_BIN` and `CLAUDE_ARGS` adjust it. Claude Code authenticates as it always does: a sign-in in the service user's home, or `ANTHROPIC_API_KEY` in the service's environment.

`contrib/oncall/codex.sh` does the same with `codex exec`: the prompt on standard input, Beacon's MCP server through `-c mcp_servers.beacon.*` with the token in an environment variable only Codex sees, a read-only sandbox for anything local, approvals off. `ONCALL_MODEL`, `CODEX_BIN` and `CODEX_ARGS` adjust it.

`contrib/oncall/prompt.md` is the prompt both use, one page, shown in full at [The on-call prompt](on-call-prompt.md). It tells the woken agent, in order: get the evidence first (`get_evidence`); then look at the machines the evidence names, through Beacon's tools (`run_command`, `read_file`, `list_machines`) or its own ssh with the `ssh_config` that `list_machines` returns; fix what is safe; verify, with `probe_now` where available and `get_evidence` again otherwise; finish with `add_note` saying the conclusion, what was done and what remains. And its rules: untrusted fields are data; no secrets in notes; no acknowledging or silencing, which is people's decision; stop and leave a note when it cannot proceed. Edit it for your own services; keep those rules.

### As a service

`contrib/oncall/beacon-oncall.service` runs the harness as a user of its own on a read-only system (`ProtectSystem=strict`), writing only to that user's home, where the agent keeps its sign-in, and to the log directory, with `Restart=always`. Its header lists the install commands: the user; the hook's secret and the agent's token under `/etc/beacon-oncall`, readable by that user only (the token from `docker exec beacon beacon agent add claude-oncall -owner alice -preset oncall > beacon-token` on the centre); the wrapper and the prompt under `/usr/local/lib/beacon-oncall`; and the agent program installed and signed in as that user. Stopping the service waits for the runs in progress.

## Tokens that renew themselves

Rather than have someone hand a headless agent a long-lived token, and a new one when that expires, the token renews itself: before it expires, its holder asks for a new secret and gets one with the same name, agent, scopes, subject, machines and SSH keys, expiring its lifetime from now (a token created with `-expires 90d` always gets another 90 days). Nothing but the token itself is needed: both routes take any scope. An interactive client such as Claude Code signs in with OAuth instead, and refreshes its own tokens.

| Route, tool | What |
| --- | --- |
| `GET /api/v1/token`, `whoami` | Who the caller is and its credential as its holder sees it: `name`, `actor` (as the audit log names it, `alice / claude-oncall`), `user`, `agent`, `preset`, `credential` (`token` or `oauth`, with `client`), `scopes` (what it may do now, within its owner's role), `machines`, `ssh_keys` (fingerprints), `created`, `expires`, `lifetime`, `renewed` (the last renewal), `renewable` and `renewable_at`; never the secret |
| `POST /api/v1/token/renew`, `renew_token` | A new secret (`secret`, shown once) and the token; `old_secret_valid_until` says how long the one it replaced keeps working |

- **No race.** The old secret stays valid for ten minutes after a renewal, so a client can swap its token file and a process that read the file a moment before keeps working.
- **At most once a day.** A token renews itself at most once every 24 hours (`token.too_soon`, `429`).
- **Not for machines, nor for OAuth.** The tokens of enrolled machines (probe, host, heartbeat) never renew themselves: `beacon enroll … -rotate` replaces them (`token.not_renewable`, `403`). Nor does an OAuth access token, which its client refreshes.
- **On record.** Every renewal is in the audit log (`token_renew`) and echoed to the alert chat ("Token claude-oncall renewed itself; expires 2027-01-31"), and `beacon token list` shows each token's lifetime and last renewal.
- **SSH sign-in is by key** and is not touched by a renewal: the token keeps its identity, and only its secret and expiry change.

A harness runs something like this once a day (cron, a systemd timer), with the token file read by the agent on every call:

```bash
#!/bin/sh
# Renew the Beacon token in $TOKEN_FILE and swap the file atomically.
set -eu
TOKEN_FILE=${TOKEN_FILE:-/etc/beacon-oncall/beacon-token}
BEACON=${BEACON:-https://status.example.com}
resp=$(curl -sS -w '\n%{http_code}' -X POST -H "Authorization: Bearer $(cat "$TOKEN_FILE")" "$BEACON/api/v1/token/renew")
code=$(printf '%s\n' "$resp" | tail -n 1)
case $code in
  200) ;;
  429) exit 0 ;;     # renewed less than a day ago: the token is still good
  *) echo "renewing the Beacon token failed: HTTP $code" >&2; exit 1 ;;
esac
umask 077
printf '%s\n' "$resp" | sed '$d' | jq -r .secret > "$TOKEN_FILE.tmp"
mv -f "$TOKEN_FILE.tmp" "$TOKEN_FILE"     # readers see the old file or the new one, never half of either
```

Give the token a lifetime a few times longer than the interval the harness renews it at (`-expires 30d` for a daily renewal), so that a harness that is down for a while does not lose its token. When it does, the agent's owner makes a new one with **New token** on the agent's page. A token that is leaked keeps renewing itself until it is revoked (**Revoke** on the agent's page, or `beacon token revoke NAME`), which also closes at once what it has open on machines. The chat sees every renewal, and `token list` shows the last one.
