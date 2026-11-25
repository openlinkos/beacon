import { Action, Facts, Field, Section, Select, Table, ToneText } from './admin'
import type { Pick } from './admin'
import type { Tone } from './api'
import { Link } from './navigation'

type Text = Record<string, string>
type PageRef = { Label: string; Href: string }

export type MonitorsData = {
  L: Text; Add: string; Config: string; ProbeError: string
  Rows: { Service: string; Href: string; State: string; Tone: Tone; Severity: string; Target: string; More: string; Checked: string; Reason: string; Alert: string; AlertHref: string; Pages: PageRef[] | null }[] | null
  Heartbeats: { Name: string; State: string; Tone: Tone; Last: string; MaxAge: string; Machine: string }[] | null
  Resources: { Name: string; Kind: string; Expires: string; Left: string; Tone: Tone; Note: string }[] | null
}

export function Monitors({ data: p }: { data: MonitorsData }) {
  const l = p.L
  return <>
    <div id="services"><Section title={l.Services} actions={p.Add && <Link className="button primary" href={p.Add}>{l.Add}</Link>}>
      {p.ProbeError && <p role="status" className="operation-error">{l.LimitedResults}</p>}
      <Table columns={[l.Service, l.State, l.Targets, l.Severity, l.Checked, l.Pages]} rows={(p.Rows ?? []).map(row => [
        <Link href={row.Href}>{row.Service}</Link>,
        <div><ToneText text={row.State} tone={row.Tone} />{row.Reason && <p>{row.Reason}</p>}
          {row.AlertHref && <Link href={row.AlertHref}>{row.Alert}</Link>}</div>,
        <span>{row.Target} {row.More}</span>, row.Severity, row.Checked,
        <ul>{row.Pages?.map(publicPage => <li key={publicPage.Href}><Link href={publicPage.Href}>{publicPage.Label}</Link></li>)}</ul>,
      ])} empty={l.NoServices} />
    </Section></div>
    <div id="heartbeats"><Section title={l.Heartbeats} actions={p.Config && <Link href="/console/config#heartbeats">{p.Config}</Link>}>
      <Table columns={[l.Name, l.State, l.LastSuccess, l.MaxAge, l.Machine]} empty={l.None} rows={(p.Heartbeats ?? []).map(row => [
        row.Name, <ToneText text={row.State} tone={row.Tone} />, row.Last, row.MaxAge, row.Machine,
      ])} />
    </Section></div>
    <div id="resources"><Section title={l.Resources} actions={p.Config && <Link href="/console/config#resources">{p.Config}</Link>}>
      <Table columns={[l.Name, l.Kind, l.Expires, l.Note]} empty={l.None} rows={(p.Resources ?? []).map(row => [
        row.Name, row.Kind, <ToneText text={row.Expires} tone={row.Tone} />, row.Note,
      ])} />
    </Section></div>
  </>
}

type Target = {
  L: Text; Set: string; Name: string; Scope: string; Module: string; Severity: string
  Domain: string; Page: string; Visibility: string; Machine: string; Edit: string; EditLabel: string; Targets: string[] | null
}

function TargetDetails({ data: p }: { data: Target }) {
  const l = p.L
  return <div className="target-details">
    <h3>{p.Set} / {p.Name}</h3>
    <Facts rows={[
      { K: l.Scope, V: p.Scope }, { K: l.Domain, V: p.Domain }, { K: l.Page, V: p.Page },
      { K: l.Visibility, V: p.Visibility }, { K: l.Machine, V: p.Machine },
    ].filter(row => row.V)} />
    <ul className="plain-list">{p.Targets?.map(target => <li key={target}><code>{target}</code></li>)}</ul>
    {p.Edit && <Link className="button" href={p.Edit}>{p.EditLabel}</Link>}
  </div>
}

export type MonitorData = {
  L: Text; LL: Text; ProbesNote: string; ProbeError: string; Refresh: string; Coverage: string; Fresh: number; Expected: number
  Targets: Target[] | null; Pages: PageRef[] | null; Schedule: string; Edit: string; EditLabel: string
  Probes: { Location: string; Instance: string; State: string; Reason: string; Detail: string; Checked: string; Tone: Tone; OK: boolean }[] | null
  Alerts: { Title: string; Href: string; Severity: string; Status: string; StatusTone: Tone; Since: string }[] | null
}

export function Monitor({ data: p }: { data: MonitorData }) {
  const l = p.L
  const ll = p.LL
  return <>
    <div className="resource-tools">
      {p.Edit && <Link className="button primary" href={p.Edit}>{p.EditLabel}</Link>}
      {p.Schedule && <Link className="button" href={p.Schedule}>{l.Schedule}</Link>}
    </div>
    <Section title={ll.Probes} actions={p.Refresh && <Link className="button" href={p.Refresh}>{l.RefreshResults}</Link>}>
      {p.Expected > 0 && <div className="coverage"><span>{p.Coverage}</span><meter min={0} max={p.Expected} value={Math.min(p.Fresh, p.Expected)} /></div>}
      {p.ProbeError && <p role="status" className="operation-error">{p.ProbeError}</p>}
      <Table columns={[ll.Location, ll.Instance, ll.Result, ll.Reason, l.Checked]} empty={p.ProbesNote} rows={(p.Probes ?? []).map(row => [
        row.Location, row.Instance, <ToneText text={row.State} tone={row.Tone || (row.OK ? 'success' : 'danger')} />,
        row.Reason || row.Detail, row.Checked,
      ])} />
    </Section>
    <Section title={l.Targets}>{p.Targets?.map((group, index) => <TargetDetails key={index} data={group} />)}</Section>
    <Section title={l.Alerts}><Table columns={[ll.Alert, ll.Severity, ll.Status, l.Started]} empty={l.None} rows={(p.Alerts ?? []).map(row => [
      <Link href={row.Href}>{row.Title}</Link>, row.Severity, <ToneText tone={row.StatusTone} text={row.Status} />, row.Since,
    ])} /></Section>
    {p.Pages?.length ? <Section title={l.Pages}><ul className="plain-list">{p.Pages.map(page => <li key={page.Href}><Link href={page.Href}>{page.Label}</Link></li>)}</ul></Section> : null}
  </>
}

export type MonitorFormData = {
  L: Text; Form: string; Remove: string; Input: { Service: string; URL: string }; Version: number
  Monitors: string; Config: string; Scopes: Pick[] | null; Modules: Pick[] | null; Severities: Pick[]; Machines: Pick[] | null
}
export function MonitorForm({ data: p }: { data: MonitorFormData }) {
  const l = p.L
  return <div className="settings-width">
    {p.Scopes?.length && p.Modules?.length ? <Action action={p.Form} label={l.Add} primary hidden={{ version: p.Version }}>
      {!p.Remove && <Field label={l.Name}><input name="service" defaultValue={p.Input.Service} maxLength={64} required /></Field>}
      <Field label={l.URL}><input name="url" type="url" defaultValue={p.Input.URL} maxLength={2048} required /></Field>
      <Field label={l.Scope}><Select name="scope" options={p.Scopes} /></Field>
      <Field label={l.Module}><Select name="module" options={p.Modules} /></Field>
      <Field label={l.Severity}><Select name="severity" options={p.Severities} /></Field>
      {p.Machines?.length ? <Field label={l.Machine}><Select name="machine" options={p.Machines} /></Field> : null}
    </Action> : <>
      {!p.Scopes?.length && <p role="alert">{l.NoLocations}</p>}
      {!p.Modules?.length && <p role="alert">{l.NoModules}</p>}
      <Link href={p.Config}>{l.Config}</Link>
    </>}
    <Link href={p.Monitors}>{l.Cancel}</Link>
    {p.Remove && <details className="disclosure standalone"><summary>{l.Remove}</summary>
      <Action action={p.Remove} label={l.Remove} danger hidden={{ version: p.Version }} />
    </details>}
  </div>
}

export function ResponseMachine({ data: p }: { data: { Group: Target | null; Action: string; Version: number; Designation: string; Machines: Pick[]; Back: string; Return: string; Save: string } }) {
  return <div className="settings-width">
    {p.Group && <>
      <TargetDetails data={p.Group} />
      <Action action={p.Action} label={p.Save} primary hidden={{ version: p.Version }}>
        <Field label={p.Designation}><Select name="machine" options={p.Machines} /></Field>
      </Action>
    </>}
    <Link href={p.Back}>{p.Return}</Link>
  </div>
}
