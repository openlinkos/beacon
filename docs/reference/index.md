# Reference

Everything Beacon takes and answers, listed for looking up. [Using Beacon](../guide/using-beacon.md) says how to use it.

The site's build checks these pages against the code. Every REST operation, gateway route, error code, scope, MCP tool, command, flag and `config.yaml` key appears in them, or the build fails.

## For agents and scripts {#for-agents-and-scripts}

1. [REST API](api.md): getting a credential, every route with the scope it needs, the gateway's routes, and every error code.
2. [MCP tools](mcp.md): connecting, and every tool with its scope.
3. [Webhooks](webhooks.md): what a hook sends: the events, the headers, the payload, the signature and the retries.

## For operators {#for-operators}

1. [Command line](cli.md): every command and flag of `beacon`, `beacon-probe` and `beacon-oncall`.
2. [config.yaml](config.md): every key of the service's own configuration.
3. [The catalogue](catalogue.md): the catalogue, target and module files, field by field, and the checks every change passes.

## How Beacon decides {#how-beacon-decides}

1. [Roles, presets and scopes](permissions.md): what people, their agents and machines' tokens may do.
2. [Metrics and alert rules](metrics.md): every metric, and every alert rule the reference deployment ships.
3. [Sources](sources.md): who sends Beacon what, to which route, with which token.
