# GitHub Actions

A hook of `kind: github` wakes a GitHub workflow: Beacon dispatches it with two inputs, `alert` (the group's id) and `event` (`firing` or `resolved`), which the workflow must declare:

```yaml
on:
  workflow_dispatch:
    inputs:
      alert: {description: Beacon alert group id, required: true}
      event: {description: firing or resolved, required: false, default: firing}

jobs:
  oncall:
    if: inputs.event == 'firing'
    runs-on: ubuntu-latest
    steps:
      - name: Evidence
        env:
          ALERT: ${{ inputs.alert }}
          BEACON_TOKEN: ${{ secrets.BEACON_TOKEN }}
        run: |
          curl -fsS -H "Authorization: Bearer $BEACON_TOKEN" \
            "https://status.example.com/api/v1/alerts/$ALERT/evidence" > evidence.json
      # then the agent's own step, with Beacon's MCP server and its own token
```

Pass the inputs through `env` as above, never into the script itself: anyone who may dispatch the workflow chooses them. A complete workflow that runs Claude Code is in [Run Claude Code in the workflow](#run-claude-code-in-the-workflow) below.

## The hook in Beacon

List the hook in Beacon's configuration:

```yaml
agents:
  - name: ops-workflow
    kind: github
    repository: example/ops
    workflow: oncall.yml
    ref: main
    token_file: /etc/beacon/secrets/github-dispatch-token
```

`repository` is `owner/name`, `workflow` the workflow's file name or id, `ref` the branch (`main` by default), and `token_file` a token that may dispatch the workflow: a fine-grained token with Actions: read and write on that repository. When the hook is called, the retries and where each call's outcome shows are in [Put an agent on call](../guide/on-call.md#wake-an-agent).

## Run Claude Code in the workflow

For a hook of `kind: github`, `contrib/github/oncall.yml` is a workflow that takes the inputs `alert` and `event`, builds the prompt from the same `prompt.md` (copied to `.github/oncall/prompt.md`), and runs Claude Code through its GitHub Action with Beacon's MCP server: the token from the repository secret `BEACON_TOKEN`, the endpoint from the variable `BEACON_MCP_URL`, the same restriction to Beacon's tools. The secret is an agent's token, made for the workflow with `beacon agent add ops-workflow -owner alice -preset oncall` ([Headless agents and CI](headless.md#make-an-agent-and-its-token)). The agent fetches everything else from Beacon with its token; the inputs pass through the environment, never into a script.
