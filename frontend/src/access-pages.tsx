import { useState } from 'react'
import { Action, Facts, Field, Section, Select, Table, ToneText, useAdmin } from './admin'
import type { Pair, Pick } from './admin'
import { CopyBlock } from './components'
import { BeaconMark, Icon } from './icons'
import { Link } from './navigation'

type Text = Record<string, string>

export type SignInData = {
  L: Text; Next: string; Form: string; Error: string; WithToken: boolean
  NoAccounts: boolean; TokenHref: string; PasswordHref: string; Username: string
}
export type OneTimeForm = {
  L: Text; Heading: string; Action: string; Code: string; Form: string; Error: string
  Broken: boolean; AskUsername: boolean; Username: string; DisplayName: string; SignInHref: string
}

function AuthShell({ children }: { children: React.ReactNode }) {
  const { t } = useAdmin()
  return (
    <div className="auth-shell">
      <div className="auth-split">
        <aside className="auth-showcase">
          <div className="auth-showcase-top">
            <a href="/" className="auth-brand">
              <BeaconMark />
              <span>Beacon</span>
            </a>
            <a href="/" className="auth-home-link">
              <Icon name="home" size="xs" />
              <span>{t('backHome') || 'Back to home'}</span>
            </a>
          </div>
          <div className="auth-showcase-body">
            <span className="auth-pill"><span className="signal-dot" aria-hidden="true" /><span>{t('signInHeadline') || 'Human & AI operations infrastructure'}</span></span>
            <h2>Beacon</h2>
            <p>{t('signInTagline') || 'Multi-location consensus probing, short-lived SSH certificates with browser replay, and a native MCP server you control.'}</p>
            <ul className="auth-highlights">
              <li><Icon name="globe" size="sm" /><span>{t('highlightProbe') || 'Multi-location consensus'}</span></li>
              <li><Icon name="terminal" size="sm" /><span>{t('highlightSSH') || '5-min SSH certificates'}</span></li>
              <li><Icon name="layers" size="sm" /><span>{t('highlightMCP') || 'Native MCP + OAuth 2.1'}</span></li>
            </ul>
          </div>
        </aside>
        <div className="auth-card-col">
          <div className="auth-card">{children}</div>
        </div>
      </div>
    </div>
  )
}

export function SignInForm({ data: p }: { data: SignInData }) {
  const l = p.L
  return (
    <AuthShell>
      <div className="auth-card-head">
        <div className="auth-brand-inline"><BeaconMark /><span>Beacon</span></div>
        <h1>{l.Submit}</h1>
      </div>
      {p.Error && <p className="operation-error" role="alert">{p.Error}</p>}
      {p.NoAccounts && <p className="notice" role="status">{l.NoAccounts}</p>}
      <Action action="/console/sign-in" label={l.Submit} primary hidden={{ next: p.Next, form: p.Form }}>
        {p.WithToken ? (
          <Field label={l.Token}><input name="token" type="password" required autoComplete="current-password" /></Field>
        ) : (
          <>
            <Field label={l.Username}><input name="username" defaultValue={p.Username} autoComplete="username" required maxLength={32} /></Field>
            <Field label={l.Password}><input name="password" type="password" autoComplete="current-password" required maxLength={1024} /></Field>
          </>
        )}
      </Action>
      {!p.NoAccounts && (
        <div className="auth-alt">
          <Link href={p.WithToken ? p.PasswordHref : p.TokenHref}>{p.WithToken ? l.WithPassword : l.WithToken}</Link>
        </div>
      )}
    </AuthShell>
  )
}

export function AccountLinkForm({ data: p }: { data: OneTimeForm }) {
  const l = p.L
  return (
    <AuthShell>
      <div className="auth-card-head">
        <div className="auth-brand-inline"><BeaconMark /><span>Beacon</span></div>
        <h1>{p.Heading}</h1>
      </div>
      {p.Error && <p className="operation-error" role="alert">{p.Error}</p>}
      {p.Broken ? (
        <Link className="button primary" href={p.SignInHref}>{l.SignIn}</Link>
      ) : (
        <Action action={p.Action} label={l.Submit} primary hidden={{ code: p.Code, form: p.Form }}>
          {p.AskUsername && (
            <>
              <Field label={l.Username}><input name="username" defaultValue={p.Username} autoComplete="username" required minLength={2} maxLength={32} /></Field>
              <Field label={l.DisplayName}><input name="display_name" defaultValue={p.DisplayName} autoComplete="name" maxLength={64} /></Field>
            </>
          )}
          <Field label={l.Password}><input name="password" type="password" autoComplete="new-password" required minLength={10} maxLength={1024} /></Field>
          <Field label={l.Again}><input name="again" type="password" autoComplete="new-password" required minLength={10} maxLength={1024} /></Field>
        </Action>
      )}
    </AuthShell>
  )
}

export type AccountData = {
  L: Text; Facts: Pair[]; Person: boolean
  SSH: (Text & { CanAdd: boolean; Keys: { fingerprint: string; comment?: string }[] | null
    Return: { Ref: string; Machine: string; Registration: string }
  }) | null
}

export function Account({ data: p }: { data: AccountData }) {
  const k = p.SSH
  return (
    <>
      <Facts rows={p.Facts} />
      {k && (
        <div id="ssh-keys">
          <Section title={k.Heading} actions={k.ReturnHref && <Link className="button" href={k.ReturnHref}>{k.ReturnLabel}</Link>}>
            {k.Result && <p className="notice" role={k.ResultTone === 'danger' ? 'alert' : 'status'}>{k.Result}</p>}
            <Table columns={[k.PublicKey, k.Remove]} rows={(k.Keys ?? []).map(row => [
              <span>{row.comment} <code>{row.fingerprint}</code></span>,
              <Action compact action="/console/account/keys/remove" label={k.Remove} hidden={{ fingerprint: row.fingerprint }} />,
            ])} empty={k.Empty} />
            {k.CanAdd && (
              <details className="disclosure">
                <summary>{k.Add}</summary>
                <Action action="/console/account/keys" label={k.Add} primary hidden={{ incident: k.Return.Ref, ssh_machine: k.Return.Machine, registration: k.Return.Registration }}>
                  <Field label={k.PublicKey}><textarea name="key" rows={2} maxLength={8000} required spellCheck={false} /></Field>
                  <Field label={k.Comment}><input name="comment" maxLength={100} /></Field>
                </Action>
              </details>
            )}
          </Section>
        </div>
      )}
      {p.Person && (
        <Section title={p.L.Change}>
          <Action action="/console/account/password" label={p.L.Save} primary>
            <Field label={p.L.Current}><input name="current" type="password" autoComplete="current-password" maxLength={1024} required /></Field>
            <Field label={p.L.New}><input name="password" type="password" autoComplete="new-password" minLength={10} maxLength={1024} required /></Field>
            <Field label={p.L.Again}><input name="again" type="password" autoComplete="new-password" minLength={10} maxLength={1024} required /></Field>
          </Action>
        </Section>
      )}
    </>
  )
}

type Person = {
  Username: string; DisplayName: string; Role: string; Disabled: boolean; Self: boolean
  Agents: string; AgentsHref: string; LastSignIn: string; Roles: Pick[]
  RoleAction: string; EnableAction: string; DisableAction: string; ResetAction: string
}
export type PeopleData = {
  L: Text; Roles: Pick[]; Rows: Person[] | null
  Invitations: { Note: string; Role: string; Expires: string; By: string; Withdraw: string }[] | null
}

export function People({ data: p }: { data: PeopleData }) {
  const l = p.L
  return (
    <>
      <details className="disclosure standalone">
        <summary>{l.Invite}</summary>
        <Action action="/console/people/invite" label={l.Send} primary>
          <Field label={l.Role}><Select name="role" options={p.Roles} /></Field>
          <Field label={l.Note}><input name="note" maxLength={100} /></Field>
        </Action>
      </details>
      <Table columns={[l.Person, l.Role, l.Status, l.LastSignIn, l.Agents, l.Change]} rows={(p.Rows ?? []).map(row => [
        <strong>{row.DisplayName} <small>{row.Username}</small></strong>,
        row.Role, <ToneText text={row.Disabled ? l.Disabled : l.Enabled} tone={row.Disabled ? 'warning' : 'success'} />,
        row.LastSignIn || l.Never, <Link href={row.AgentsHref}>{row.Agents}</Link>,
        !row.Self && (
          <details className="row-actions">
            <summary>{l.Change}</summary>
            <div className="row-action-list">
              <Action compact action={row.RoleAction} label={l.Change}><Field label={l.Role}><Select name="role" options={row.Roles} /></Field></Action>
              <Action compact action={row.ResetAction} label={l.Reset} />
              <Action compact action={row.Disabled ? row.EnableAction : row.DisableAction} label={row.Disabled ? l.Enable : l.Disable} danger={!row.Disabled} />
            </div>
          </details>
        ),
      ])} />
      <Section title={l.Pending}>
        <Table columns={[l.Note, l.Role, l.By, l.Expires, l.Withdraw]} rows={(p.Invitations ?? []).map(row => [
          row.Note, row.Role, row.By, row.Expires, <Action compact action={row.Withdraw} label={l.Withdraw} />,
        ])} empty={l.NoInvitations} />
      </Section>
    </>
  )
}

// Critical names the critical machines the agent names, which the form keeps as they are
type MachineAccess = { All: boolean; Selected: boolean; CanAll: boolean; Options: Pick[] | null; Critical?: string }
type AgentPermissions = { L: Text; Presets: Pick[]; MachineAccess: MachineAccess; ScopeOptions: Pick[] | null; Custom?: boolean; CanCustom: boolean }

function PermissionFields({ data: p }: { data: AgentPermissions }) {
  const l = p.L
  const [preset, setPreset] = useState(p.Presets.find(preset => preset.Selected)?.Value ?? (p.Custom ? 'custom' : ''))
  const [range, setRange] = useState(p.MachineAccess.Selected || !p.MachineAccess.CanAll ? 'selected' : 'all')
  return (
    <>
      {p.Presets.length ? (
        <Field label={l.Preset}>
          <select name="preset" value={preset} onChange={event => setPreset(event.target.value)} required>
            {!preset && <option value="" disabled>{l.Preset}</option>}
            {p.Presets.map(option => <option key={option.Value} value={option.Value}>{option.Label}</option>)}
          </select>
        </Field>
      ) : p.Custom && <input type="hidden" name="preset" value="custom" />}
      {preset === 'custom' && p.CanCustom && (
        <fieldset>
          <legend>{l.CustomScopes}</legend>
          {p.ScopeOptions?.map(scope => (
            <label className="choice" key={scope.Value}>
              <input type="checkbox" defaultChecked={scope.Selected} name="scopes" value={scope.Value} />{scope.Label}
            </label>
          ))}
        </fieldset>
      )}
      <input type="hidden" name="machine_access_form" value="1" />
      <Field label={l.AccessRange}>
        <select name="machine_access" value={range} onChange={event => setRange(event.target.value)}>
          <option value="all" disabled={!p.MachineAccess.CanAll}>{l.AllCurrentAndFuture}</option>
          <option value="selected">{l.SelectedMachines}</option>
        </select>
      </Field>
      {p.MachineAccess.Critical && <p className="notice" role="status">{p.MachineAccess.Critical}. {l.CriticalKept}</p>}
      {range === 'selected' && (
        <fieldset>
          <legend>{l.Machines}</legend>
          {p.MachineAccess.Options?.map(machine => (
            <label className="choice" key={machine.Value}>
              <input type="checkbox" defaultChecked={machine.Selected} name="machine" value={machine.Value} />{machine.Label}
            </label>
          ))}
        </fieldset>
      )}
    </>
  )
}

export type AgentsData = AgentPermissions & {
  Rows: { Name: string; Href: string; Owner: string; Preset: string; Machines: string; Created: string }[] | null
  CanCreate: boolean; ShowOwner: boolean; Owners: Pick[] | null; Expiries: Pick[]
  ClaudeCmd: string; CodexCmd: string; HooksHref: string
}

export function Agents({ data: p }: { data: AgentsData }) {
  const { t } = useAdmin()
  const l = p.L
  return (
    <>
      <div className="resource-tools">
        {p.CanCreate && (
          <details className="disclosure">
            <summary>{l.Create}</summary>
            <Action action="/console/agents" label={l.Create} primary>
              {p.Owners?.length ? <Field label={l.Owner}><Select options={p.Owners} name="owner" /></Field> : null}
              <Field label={l.Name}><input name="name" minLength={2} maxLength={40} required /></Field>
              <PermissionFields data={p} />
              <Field label={l.Expires}><Select options={p.Expiries} name="expires" /></Field>
            </Action>
          </details>
        )}
        <Link className="button" href="/workspace/capabilities">
          <Icon name="layers" size="xs" />
          <span>{t('mcpHub')}</span>
        </Link>
        {p.HooksHref && (
          <Link className="button" href={p.HooksHref}>
            <Icon name="zap" size="xs" />
            <span>{l.Hooks}</span>
          </Link>
        )}
      </div>
      <Table columns={[l.Agent, l.Owner, l.Preset, l.Machines]} rows={(p.Rows ?? []).map(row => [
        <Link href={row.Href}><strong>{row.Name}</strong></Link>, row.Owner, <span className="mono-chip">{row.Preset}</span>, row.Machines,
      ])} empty={l.NoAgents} />
      <Section title={l.OAuth}>
        <div className="hub-snippets">
          {p.ClaudeCmd && <CopyBlock title="Claude Code" label="MCP + OAuth 2.1" code={p.ClaudeCmd} copyLabel={t('copy')} copiedLabel={t('copied')} />}
          {p.CodexCmd && <CopyBlock title="Codex" label="MCP + OAuth 2.1" code={p.CodexCmd} copyLabel={t('copy')} copiedLabel={t('copied')} />}
        </div>
      </Section>
    </>
  )
}

export type AgentData = AgentPermissions & {
  Facts: Pair[]; Scopes: string[] | null; Expiries: Pick[]; SSHConfig: string
  SettingsAction: string; TokenAction: string; KeyAction: string; RemoveKey: string; Delete: string
  Tokens: { Name: string; Expires: string; Revoke: string }[] | null
  Grants: { Client: string; LastUsed: string; Expires: string; Revoke: string }[] | null
  Keys: { Fingerprint: string; Comment: string; Added: string }[] | null
}

export function Agent({ data: p }: { data: AgentData }) {
  const { t } = useAdmin()
  const l = p.L
  return (
    <>
      <Facts rows={p.Facts} />
      <Section title={l.Settings}>
        {p.SettingsAction && <Action action={p.SettingsAction} label={l.Save} primary><PermissionFields data={p} /></Action>}
        {p.Scopes?.length ? <ul className="token-list">{p.Scopes.map(scope => <li key={scope}>{scope}</li>)}</ul> : null}
      </Section>
      <Section title={l.Tokens}>
        <Table columns={[l.Name, l.Until, l.Revoke]} rows={(p.Tokens ?? []).map(row => [
          row.Name, row.Expires, row.Revoke && <Action compact action={row.Revoke} label={l.Revoke} danger />,
        ])} empty={l.NoTokens} />
        {p.TokenAction && <Action action={p.TokenAction} label={l.NewToken}><Field label={l.Expires}><Select name="expires" options={p.Expiries} /></Field></Action>}
      </Section>
      <Section title={l.Grants}>
        <Table columns={[l.Client, l.LastUsed, l.Until, l.Revoke]} rows={(p.Grants ?? []).map(row => [
          row.Client, row.LastUsed, row.Expires, row.Revoke && <Action compact action={row.Revoke} label={l.Revoke} danger />,
        ])} empty={l.NoGrants} />
      </Section>
      <Section title={l.Keys}>
        <Table columns={[l.Fingerprint, l.Comment, l.Remove]} rows={(p.Keys ?? []).map(row => [
          <code>{row.Fingerprint}</code>, row.Comment, p.RemoveKey && <Action compact action={p.RemoveKey} label={l.Remove} hidden={{ fingerprint: row.Fingerprint }} />,
        ])} empty={l.NoKeys} />
        {p.KeyAction && (
          <Action action={p.KeyAction} label={l.AddKey}>
            <Field label={l.Key}><textarea name="key" rows={2} maxLength={8000} required /></Field>
            <Field label={l.Comment}><input name="comment" maxLength={100} /></Field>
          </Action>
        )}
        {p.SSHConfig && (
          <details className="disclosure">
            <summary>{l.SSHConfig}</summary>
            <CopyBlock title="ssh_config" code={p.SSHConfig} copyLabel={t('copy')} copiedLabel={t('copied')} />
          </details>
        )}
      </Section>
      {p.Delete && (
        <details className="disclosure standalone">
          <summary>{l.Delete}</summary>
          <Action action={p.Delete} label={l.Delete} danger />
        </details>
      )}
    </>
  )
}

export type OnceData = { Value: string; Warning: string; Snippets: { Name?: string; text: string }[] | null; Back: string; BackHref: string }
export function Once({ data }: { data: OnceData }) {
  const { t } = useAdmin()
  return (
    <div className="admin-content">
      {data.Warning && <p className="notice" role="status">{data.Warning}</p>}
      <CopyBlock code={data.Value} copyLabel={t('copy')} copiedLabel={t('copied')} />
      {data.Snippets?.map((snippet, index) => (
        <CopyBlock key={index} title={snippet.Name} code={snippet.text} copyLabel={t('copy')} copiedLabel={t('copied')} />
      ))}
      <Link className="button primary" href={data.BackHref}>{data.Back}</Link>
    </div>
  )
}
