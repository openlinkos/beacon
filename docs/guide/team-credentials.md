# Use a team credential

Keep a service's bearer token in Beacon so an owner can use it in a fixed HTTPS request or delegate that request to an agent. The service receives the token. The person or agent receives an HTTP status code. Each use is committed to the audit log before Beacon sends the request.

## Enable the store

An administrator generates a dedicated key file with 64 hexadecimal digits and gives the Beacon process read access. Update `config.yaml` and restart:

```yaml
credentials:
  enabled: true
  key_file: /etc/beacon/secrets/team-credential-key
  allowed_hosts: [api.example.com]
```

Enable this feature on one Beacon centre using SQLite or PostgreSQL. Only approve hosts that your team trusts to receive bearer tokens. Use the exact authority, including a port if written in the endpoint. Keep the key and the newest known-good rollback anchor independently of the database backup. Follow [Backup and restore](../self-hosting/backup.md#restore) before relying on the store.

If Beacon cannot validate the key or policy, the credential store is unavailable and monitoring and SSH continue. A change that has reached only the database is shown as awaiting verification. Repair the anchor and reload the console before trying the change again.

## Store and use a token

1. Sign in with an owner account and open **Team credentials**.
2. Enter a name, an approved HTTPS endpoint such as `https://api.example.com/ready`, and the bearer token. Use the console to enter values directly. Endpoints take no query, credentials in the URL, or fragment.
3. Open the credential. **Send test request** performs an authenticated GET under your account. The upstream status confirms what the service returned. **View audit entries** shows the request under your name.
4. Use **Replace token** when the service issues a new token. The endpoint stays fixed and existing grants remain in force.

## Delegate and revoke

1. Give a custom agent owned by an owner the `secrets:use` scope and issue it a headless token. Existing presets, including standard OAuth sign-ins, keep their previous scopes.
2. Under the credential's **Delegate use** section, choose that agent and set a duration. You can shorten a grant or revoke it from the same page.
3. The agent can call `list_team_credentials` and `use_team_credential`, or the equivalent [REST operations](../reference/api.md#team-credentials). A grant takes effect immediately. Its expiry, revocation or the agent owner's role change prevents new uses. An already authorised request may finish.

All deployment owners can use the credential personally. This first workflow covers service bearer tokens for fixed requests. It leaves delegated human access, credential reveal, imports, browser autofill, and other uses of a team password manager as separate work.
