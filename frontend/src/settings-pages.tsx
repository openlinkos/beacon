import { useState } from 'react'
import { Action, Facts, Field, Pagination, Section, Select, Table, ToneText } from './admin'
import type { Pick } from './admin'
import { Link } from './navigation'
import { ListFilters } from './list-pages'
import type { ListData } from './list-pages'

type Text = Record<string, string>

type Credential = { id: string; name: string; endpoint: string; version: number }
type CredentialsData = {
  L: Text; Base: string; Audit: string; Rotate: string; Grant: string; Revoke: string; Test: string
  Rows: Credential[] | null; Selected: Credential | null
  Grants: { agent: string; owner: string; Ends: string }[] | null
  Agents: Pick[] | null; Durations: Pick[] | null
}

export function Credentials({ data: p }: { data: CredentialsData }) {
  const l = p.L
  const version = p.Selected?.version
  const token = (label: string) => <Field label={label}><input name="value" type="password" autoComplete="new-password" minLength={8} maxLength={4096} required /></Field>
  if (!p.Selected) return <>
    <Table columns={[l.Name, l.Endpoint]} empty={l.None} rows={(p.Rows ?? []).map(row => [
      <Link href={p.Base + '/' + encodeURIComponent(row.id)}>{row.name}</Link>, <code>{row.endpoint}</code>,
    ])} />
    <Section title={l.Add}>
      <Action action={p.Base} label={l.Save} primary>
        <Field label={l.Name}><input name="name" maxLength={100} required /></Field>
        <Field label={l.Endpoint}><input name="endpoint" type="url" maxLength={2000} required /></Field>
        {token(l.Value)}
      </Action>
    </Section>
  </>
  return <>
    <Facts rows={[{ K: l.Endpoint, V: p.Selected.endpoint }]} />
    <div className="resource-tools"><Action action={p.Test} label={l.Test} compact /><Link className="button" href={p.Audit}>{l.Record}</Link></div>
    <Section title={l.Grant}>
      <Table columns={[l.Agent, l.Owner, l.Expires, '']} empty={l.NoGrants} rows={(p.Grants ?? []).map(row => [
        row.agent, row.owner, row.Ends, <Action action={p.Revoke} label={l.Revoke} compact hidden={{ agent: row.agent, version }} />,
      ])} />
      {p.Agents?.length ? <Action action={p.Grant} label={l.Grant} primary hidden={{ version }}>
        <Field label={l.Agent}><Select name="agent" options={p.Agents} /></Field>
        <Field label={l.Until}><Select name="hours" options={p.Durations} /></Field>
      </Action> : <p>{l.NoAgents}</p>}
    </Section>
    <details className="disclosure"><summary>{l.Rotate}</summary><Action action={p.Rotate} label={l.Rotate} hidden={{ version }}>
      <p>{l.Context}</p>{token(l.NewValue)}
    </Action></details>
  </>
}

type TokenRow = Text & { Scopes: string[] | null; Keys: string[] | null; Expired: boolean; Legacy: boolean; ClaimPresets: Pick[] | null }
type TokensData = { L: Text; List: ListData; Rows: TokenRow[] | null; Owners: Pick[] | null; Presets: Pick[] | null }

export function Tokens({ data: p }: { data: TokensData }) {
  const l = p.L
  return <>
    <ListFilters list={p.List} />
    <Table columns={[l.Name, l.Scopes, l.Created, l.Expires, '']} rows={(p.Rows ?? []).map(row => [
      <><strong>{row.Name}</strong>{row.AgentHref && <small><Link href={row.AgentHref}>{row.Agent}</Link></small>}
        {row.Legacy && <small>{l.Legacy}</small>}</>, <code>{row.Scopes?.join(', ')}</code>,
      <>{row.Created}<small>{row.CreatedBy}</small></>,
      <ToneText text={row.Expires} tone={row.Expired ? 'danger' : ''} />,
      <details className="disclosure"><summary>{l.Subject}</summary>
        <Facts rows={[
          { K: l.Subject, V: row.Subject }, { K: l.Machines, V: row.Machines }, { K: l.Renewed, V: row.Renewed },
          { K: l.Keys, V: row.Keys?.join(', ') || '' },
        ].filter(item => item.V)} />
        {row.Claim && <Action action={row.Claim} label={l.Claim}>
          <p>{l.ClaimHint}</p>
          <Field label={l.Owner}><Select name="owner" options={p.Owners} /></Field>
          <Field label={l.AgentName}><input name="agent" /></Field>
          <Field label={l.Preset}><Select name="preset" options={[{ Value: '', Label: l.KeepScopes, On: true, Help: '' }, ...(row.ClaimPresets ?? [])]} /></Field>
        </Action>}
        {row.Revoke && <Action action={row.Revoke} label={l.Revoke} danger><p>{l.RevokeHint}</p></Action>}
      </details>,
    ])} />
    <Pagination pager={p.List.Pager} />
  </>
}

type ConfigData = {
  L: Text; Older: string; History: Text[] | null
  Locations: (Text & { Standby: boolean; Expected: boolean })[] | null
  Heartbeats: Text[] | null; Resources: Text[] | null; Targets: Text[] | null; Modules: Text[] | null
}

export function Config({ data: p }: { data: ConfigData }) {
  const l = p.L
  return <>
    <Section title={l.History}>
      <Table columns={[l.Version, l.Time, l.Who, l.Action, l.Items, l.Note]} empty={l.Empty} rows={(p.History ?? []).map(row => [
        <Link href={row.Href}>{row.Version}</Link>, row.Time, <>{row.Who}<small>{row.Via}</small></>, row.Action, row.Items, row.Note,
      ])} />
      {p.Older && <Link className="button" href={p.Older}>{l.Older}</Link>}
    </Section>
    <Section title={l.Locations}>
      <Table columns={[l.ID, l.Name, l.Region, l.Network, l.Standby, l.Expected, l.Reporting]} empty={l.Empty} rows={(p.Locations ?? []).map(row => [
        row.ID, row.Name, row.Region, row.Network, row.Standby ? l.Yes : l.No, row.Expected ? l.Yes : l.No, row.Reporting,
      ])} />
    </Section>
    <Section title={l.Heartbeats}><Table columns={[l.Name, l.MaxAge, l.Severity, l.Description]} empty={l.Empty}
      rows={(p.Heartbeats ?? []).map(row => [row.Name, row.MaxAge, row.Severity, row.Description])} /></Section>
    <Section title={l.Resources}><Table columns={[l.Name, l.Kind, l.Expires, l.Note]} empty={l.Empty}
      rows={(p.Resources ?? []).map(row => [row.Name, row.Kind, row.Expires, row.Note])} /></Section>
    <Section title={l.Targets}><Table columns={[l.Name, l.ProbedBy, l.Groups, l.TargetsN, l.Modules, l.Services]} empty={l.Empty}
      rows={(p.Targets ?? []).map(row => [row.Name, row.ProbedBy, row.Groups, row.Targets, row.Modules, row.Services])} /></Section>
    <Section title={l.Modules}><Table columns={[l.Name, l.Prober]} empty={l.Empty}
      rows={(p.Modules ?? []).map(row => [row.Name, row.Prober])} /></Section>
  </>
}

type VersionData = {
  L: Text; Change: Text; Revert: string; Reverts: string
  Items: { Kind: string; Key: string; Op: string; Lines: { Op: string; Text: string }[] }[] | null
}

export function Version({ data: p }: { data: VersionData }) {
  const l = p.L
  return <>
    <Facts rows={[{ K: l.Time, V: p.Change.Time }, { K: l.Who, V: p.Change.Who }, { K: l.Via, V: p.Change.Via },
      { K: l.Note, V: p.Change.Note }, { K: l.Action, V: p.Reverts }].filter(row => row.V)} />
    {p.Items?.length ? p.Items.map(item => <Section key={item.Kind + '/' + item.Key} title={item.Kind + ' ' + item.Key} actions={<code>{item.Op}</code>}>
      <pre className="change-diff">{item.Lines.map((line, index) => <span key={index} data-tone={line.Op === '+' ? 'success' : line.Op === '-' ? 'danger' : undefined}>{line.Op} {line.Text}{'\n'}</span>)}</pre>
    </Section>) : <p>{l.Empty}</p>}
    {p.Revert && <details className="disclosure"><summary>{l.Revert}</summary>
      <Action action={p.Revert} label={l.Revert} danger><p>{l.RevertHint}</p>
        <Field label={l.RevertNote}><input name="note" maxLength={500} /></Field>
      </Action>
    </details>}
  </>
}

type ShareLinksData = { L: Text; Resource: string; SessionID: string; Form: string; Audience: string; Revocation: string; IssuedURL: string; Expiry: string; ExpiryISO: string; Back: string }

export function ShareLinks({ data: p }: { data: ShareLinksData }) {
  const l = p.L
  const [copied, setCopied] = useState('')
  return <>
    <Facts rows={[{ K: l.Subject, V: p.Resource, Href: p.Back }, { K: l.SessionID, V: p.SessionID }].filter(row => row.V)} />
    <p>{p.Audience}</p>
    {p.IssuedURL ? <Section title={l.Created}>
      <pre>{p.IssuedURL}</pre>
      {p.Expiry && <p>{l.Expires} <time dateTime={p.ExpiryISO}>{p.Expiry}</time></p>}
      <button className="button primary" type="button" onClick={() => {
        navigator.clipboard.writeText(p.IssuedURL).then(() => setCopied(l.Copied), () => setCopied(l.CopyError))
      }}>{l.Copy}</button>
      <span role="status">{copied}</span>
    </Section> : <Action action={p.Form} label={l.Create} intent="create" primary compact />}
    <details className="disclosure"><summary>{l.RevokeHeading}</summary>
      <Action action={p.Form} label={l.Revoke} intent="revoke" danger><p>{p.Revocation}</p></Action>
    </details>
  </>
}
