import { useState } from 'react'
import { Action, Discovery, Facts, Field, Pagination, Section, Table, ToneText } from './admin'
import type { Pair } from './admin'
import type { Tone } from './api'
import { Link } from './navigation'

type Text = Record<string, string>
type Pager = { Summary: string; Prev: string; Next: string }
export type MachineList = {
  L: Text; Channel: { On: boolean; Paused: boolean; Can: boolean; State: string; Since: string }
  List: { Query: string; Pager: Pager }
  Rows: { Name: string; Href: string; Address: string; ConnectorTone: Tone; Sessions: string; Last: string; LastHref: string; ConnectLabel: string; Critical: boolean }[] | null
}

function RegisterMachine({ l }: { l: Text }) {
  const [byConnector, setByConnector] = useState(false)
  return <Action action="/console/machines" label={l.Add} primary>
    <Field label={l.Name}><input name="name" maxLength={63} required /></Field>
    <Field label={l.Connection}><select name="reach" value={byConnector ? 'connector' : 'dial'} onChange={event => setByConnector(event.target.value === 'connector')}>
      <option value="dial">{l.Dial}</option><option value="connector">{l.Behind}</option>
    </select></Field>
    {byConnector ? <>
      <Field label={l.ConnectorKey}><textarea name="connector_key" maxLength={8000} required rows={2} /></Field>
      <Field label={l.HostKeyOpt}><textarea name="host_key" maxLength={8000} rows={2} /></Field>
    </> : <Field label={l.Address}><input name="address" maxLength={253} required /></Field>}
    <Field label={l.User}><input name="user" maxLength={32} /></Field>
    <Field label={l.Note}><input name="note" maxLength={2000} /></Field>
    <label className="choice"><input type="checkbox" name="critical" value="on" />{l.CriticalMachine}</label>
  </Action>
}

export function Machines({ data: p }: { data: MachineList }) {
  const l = p.L
  const c = p.Channel
  return <>
    <div className="resource-tools">
      <Discovery action="/console/machines" search={l.SearchMachines} value={p.List.Query} />
      {c.On && c.Can && <details className="disclosure"><summary>{l.Add}</summary><RegisterMachine l={l} /></details>}
    </div>
    <Table columns={[l.Name, l.Address, l.Sessions24h, l.LastSession, l.Connect]} rows={(p.Rows ?? []).map(row => [
      <Link href={row.Href}>{row.Name}{row.Critical && <span className="r-critical"> {l.Critical}</span>}</Link>,
      <ToneText text={row.Address} tone={row.ConnectorTone} />, row.Sessions,
      row.LastHref ? <Link href={row.LastHref}>{row.Last}</Link> : row.Last,
      row.ConnectLabel && <Link className="button" href={row.Href + '#ssh-connect'} aria-label={row.ConnectLabel}>SSH</Link>,
    ])} empty={p.List.Query ? l.NoMatch : l.NoMachines} />
    <Pagination pager={p.List.Pager} />
    <Section title={l.Channel}>
      {c.On ? <ToneText text={c.State} tone={c.Paused ? 'warning' : 'success'} /> : <p role="status">{l.Off}</p>}
      {c.Since && <p>{c.Since}</p>}
      {c.On && c.Can && <details className="disclosure"><summary>{c.Paused ? l.Resume : l.Pause}</summary>
        {!c.Paused && <p>{l.PauseHint}</p>}
        <Action compact action={c.Paused ? '/console/machines/resume' : '/console/machines/pause'} label={c.Paused ? l.Resume : l.Pause} danger={!c.Paused} />
      </details>}
    </Section>
  </>
}

type MachineConnection = Text & { Check: { Title: string; Detail: string; At: string; Ready: boolean } }
export type MachineData = {
  L: Text; LL: Text; Facts: Pair[]; Note: string; Sessions: SessionRecord[] | null
  Commands: { At: string; Agent: string; Command: string; Exit: string; Took: string; Href: string; Failed: boolean }[] | null
  Can: boolean; User: string; Critical: boolean; Connector: boolean
  EditAction: string; ReplaceAction: string; ConnectorAction: string; RemoveAction: string
  OldFP: string; NewFP: string; Connection: MachineConnection | null; History: string; HistoryHref: string
  MetadataVersion: number; SessionDetails: boolean; SessionHint: string; List: { Pager: Pager }
}

export type SessionRecord = {
  ID: string; Agent: string; Machine: string; MachineHref: string; Started: string; Href: string; Kind: string
  Via: string; Duration: string; Command: string; Exit: string; Failed: boolean
  RecordingState: { Text: string; Tone: Tone }; TranscriptState: { Text: string; Tone: Tone }
}
export function SessionRows({ rows, l, details, empty }: { rows?: SessionRecord[] | null; l: Text; details: boolean; empty?: string }) {
  return <Table columns={[l.Started, l.Actor, l.Machine, l.Kind, ...(details ? [l.Command, l.Evidence] : []), l.Duration, l.Exit]}
    rows={(rows ?? []).map(row => [
      row.Href ? <Link href={row.Href}>{row.Started}</Link> : row.Started, <span>{row.Agent} <small>{row.Via}</small></span>,
      <Link href={row.MachineHref}>{row.Machine}</Link>, row.Kind,
      ...(details ? [<code>{row.Command}</code>, <div>
        <ToneText text={row.RecordingState.Text} tone={row.RecordingState.Tone} />
        <ToneText text={row.TranscriptState.Text} tone={row.TranscriptState.Tone} />
      </div>] : []),
      row.Duration, <ToneText text={row.Exit} tone={row.Failed ? 'danger' : ''} />,
    ])} empty={empty} />
}

export function Machine({ data: p }: { data: MachineData }) {
  const l = p.L
  const link = p.Connection
  const [copied, setCopied] = useState('')
  return <>
    <Facts rows={p.Facts} />
    {p.Note && <p>{p.Note}</p>}
    <Facts rows={p.NewFP ? [{ K: l.Previous, V: p.OldFP }, { K: l.NewFP, V: p.NewFP }] : []} />
    {link && <div id="ssh-connect"><Section title={link.Title} actions={link.IncidentHref && <Link href={link.IncidentHref}>{link.IncidentLabel}</Link>}>
      {link.Href && <Link className="button" href={link.Href}>{link.Link}</Link>}
      {link.Command && <><pre>{link.Command}</pre>
        <button className="button" onClick={() => void navigator.clipboard.writeText(link.Command).then(() => setCopied(link.Copied), () => setCopied(link.CopyError))}>{link.Copy}</button>
        {copied && <span role="status">{copied}</span>}
        <details className="disclosure"><summary>{link.Setup}</summary><pre>{link.Config}</pre></details>
      </>}
      {link.CheckAction && <Action action={link.CheckAction} label={link.CheckLabel} />}
      {link.Check.Title && <p role="status"><ToneText text={link.Check.Title} tone={link.Check.Ready ? 'success' : 'warning'} /> {link.Check.Detail} {link.Check.At}</p>}
    </Section></div>}
    <Section title={l.Sessions} actions={<Link href={p.HistoryHref}>{p.History}</Link>}>
      <SessionRows rows={p.Sessions} l={p.LL} details={p.SessionDetails} empty={p.SessionHint || l.None} />
      <Pagination pager={p.List.Pager} />
    </Section>
    {p.SessionDetails && p.Commands?.length ? <Section title={l.Commands}>
      <Table columns={[p.LL.Time, p.LL.Command, p.LL.Actor, p.LL.Exit, p.LL.Duration]} rows={p.Commands.map(row => [
        <Link href={row.Href}>{row.At}</Link>, <code>{row.Command}</code>, row.Agent, <ToneText text={row.Exit} tone={row.Failed ? 'danger' : ''} />, row.Took,
      ])} />
    </Section> : null}
    {p.Can && <Section title={l.Edit}>
      <Action action={p.EditAction} label={l.Save} hidden={{ if_version: p.MetadataVersion }}>
        <Field label={l.User}><input name="user" defaultValue={p.User} maxLength={32} required /></Field>
        <label className="choice"><input type="checkbox" name="critical" value="on" defaultChecked={p.Critical} />{l.Critical}</label>
        <Field label={l.Note}><input name="note" defaultValue={p.Note} maxLength={2000} /></Field>
      </Action>
      {p.Connector ? <details className="disclosure"><summary>{l.ChangeKey}</summary>
        <Action action={p.ConnectorAction} label={l.ChangeKey}>
          <Field label={l.ConnectorKey}><textarea name="key" rows={2} required maxLength={8000} /></Field>
        </Action>
      </details> : <details className="disclosure"><summary>{l.PinAgain}</summary>
        <Action action={p.ReplaceAction} label={l.Pin} />
      </details>}
      <details className="disclosure"><summary>{l.RemoveMachine}</summary><Action action={p.RemoveAction} label={l.Remove} danger>
        <Field label={l.Confirm}><input name="confirm" required maxLength={63} /></Field>
      </Action></details>
    </Section>}
  </>
}

type HistoryRow = { Now?: string; Version: number; Current: boolean; Who: string; Time: string; Change: string; User: string; Critical: string; Note: string; Restore: string }
export function MachineHistory({ data: p }: { data: { L: Text; Rows: HistoryRow[] | null; Post: string; Current: number; CanRestore: boolean; Older: string } }) {
  const l = p.L
  return <>
    <Table columns={[l.Version, l.Change, l.Who, l.Time, l.Account, l.Critical, l.Note, l.Restore]} empty={l.Empty}
      rows={(p.Rows ?? []).map(row => [
        row.Version, row.Change, row.Who, row.Time, row.User, row.Critical, row.Note,
        row.Current ? l.Current : p.CanRestore && <Action compact action={p.Post} label={row.Restore} hidden={{ version: row.Version, if_version: p.Current }} />,
      ])} />
    {p.Older && <Link href={p.Older}>{l.Older}</Link>}
  </>
}
