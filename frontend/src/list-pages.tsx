import { Facts, Field, Pagination, Section, Select, Table, ToneText, visit } from './admin'
import type { Pick } from './admin'
import type { Tone } from './api'
import { SessionRows } from './machine-pages'
import type { SessionRecord } from './machine-pages'
import { Link } from './navigation'

type Text = Record<string, string>

export type ListData = {
  Path: string; SearchLabel: string; SearchHint: string; Query: string; Empty: string
  Hidden: { K: string; V: string }[] | null
  Tags: { Label: string; Href: string }[] | null
  Facets: { Title: string; Values: { Label: string; Href: string; On: boolean; Count: string }[] | null }[] | null
  Pager: { Summary: string; Prev: string; Next: string }
}

export function ListFilters({ list }: { list: ListData }) {
  return <>
    <div className="resource-tools">
      {list.Path && list.SearchLabel && <form role="search" className="resource-search" key={list.Query}
        onSubmit={event => {
          event.preventDefault()
          const query = new URLSearchParams()
          for (const [name, value] of new FormData(event.currentTarget)) if (typeof value === 'string') query.append(name, value)
          if (!query.get('q')?.trim()) query.delete('q')
          visit(list.Path + (query.size ? '?' + query : ''))
        }}>
        {list.Hidden?.map((param, index) => <input key={index} type="hidden" name={param.K} value={param.V} />)}
        <input type="search" name="q" aria-label={list.SearchLabel} placeholder={list.SearchHint || list.SearchLabel} defaultValue={list.Query} />
        <button className="button">{list.SearchLabel}</button>
      </form>}
      {list.Facets?.map(facet => <details className="disclosure facet" key={facet.Title}><summary>{facet.Title}</summary>
        <ul className="plain-list">{facet.Values?.map(value => <li key={value.Href}><Link href={value.Href} aria-current={value.On ? 'true' : undefined}>{value.Label} {value.Count}</Link></li>)}</ul>
      </details>)}
    </div>
    {list.Tags?.length ? <div className="filter-tags">{list.Tags.map(tag => <Link key={tag.Href} href={tag.Href}>{tag.Label} ×</Link>)}</div> : null}
  </>
}

type LogData = { L: Text; List: ListData; Rows: Text[] | null; IncidentHref?: string; IncidentTitle?: string; Hooks?: (Text & { Resolved: boolean })[] | null }

export function Log({ data: p, route }: { data: LogData; route: string }) {
  const l = p.L
  const rows = p.Rows ?? []
  const audit = route === 'audit'
  const hooks = route === 'hooks'
  return <>
    {p.IncidentHref && <div className="resource-tools"><Link className="button" href={p.IncidentHref}>{p.IncidentTitle}</Link></div>}
    {hooks && <Section title={l.Configured}>
      <Table columns={[l.Hook, l.Kind, l.Target, l.Severities, l.Service, l.Resolved, l.Calls]} empty={l.NoHooks}
        rows={(p.Hooks ?? []).map(row => [row.Name, row.Kind, row.Where, row.Severities, row.Service, row.Resolved ? l.Yes : l.No, row.Calls])} />
    </Section>}
    <ListFilters list={p.List} />
    {audit ? <Table columns={[l.Time, l.Actor, l.Action, l.Target, l.Detail]} empty={p.List.Empty}
      rows={rows.map(row => [row.Time, <>{row.Actor}<small>{row.Via}</small></>, row.Action, row.Target, row.Detail])} />
      : hooks ? <Table columns={[l.When, l.Hook, l.Event, l.Status, l.Code, l.Attempts, l.Alert]} empty={p.List.Empty}
        rows={rows.map(row => [row.When, row.Hook, row.Event, <><ToneText text={row.Status} tone={row.StatusTone as Tone} /><small>{row.Error}</small></>,
          row.Code, row.Attempts, row.AlertHref ? <Link href={row.AlertHref}>{row.Alert}</Link> : row.Alert])} />
        : <Table columns={[l.Time, l.Source, l.Kind, l.Service, l.Machine, l.Summary]} empty={p.List.Empty}
          rows={rows.map(row => [row.Time, row.Source, row.Kind, row.Service, row.Machine,
            row.Href ? <a href={row.Href}>{row.Summary}</a> : row.Summary])} />}
    <Pagination pager={p.List.Pager} />
  </>
}

type Filters = { Name: string; Label: string; Options: Pick[] | null }[] | null
type HistoryRow = {
  Title: string; Href: string; Service: string; Services: string; Severity: string; SeverityTone: Tone; Started: string; Resolved: string
  State: string; Tone: Tone; Origin: string; OpeningUnknown: string; Wake: string; Verification: string
  AgentReport: boolean; HumanAck: boolean; PassingCheck: boolean; AgentPassingCheck: boolean; Exercise: boolean
  Human: string; Passing: string; Programmes: { Title: string; Status: string; Exit: string }[] | null; Participants: string[] | null
}
type HistoryData = {
  L: Text; Rows: HistoryRow[] | null; Filters: Filters; Group: string; AllGroups: string; Reset: string; Older: string
  First: string; Episodes: string; EpisodesLabel: string; History: string; HistoryLabel: string
  Since: string; Until: string; SinceLabel: string; UntilLabel: string; Before: string; Status: string; Empty: string; Coverage: string
}

export function History({ data: p, episodes }: { data: HistoryData; episodes: boolean }) {
  const l = p.L
  const rows = p.Rows ?? []
  const other = episodes ? p.History : p.Episodes
  const route = '/console/incident-' + (episodes ? 'episodes' : 'outcomes')
  return <>
    {other && <div className="resource-tools"><Link className="button" href={other}>{episodes ? p.HistoryLabel : p.EpisodesLabel}</Link></div>}
    <form className="resource-tools history-filters" onSubmit={event => {
      event.preventDefault()
      const query = new URLSearchParams()
      for (const [name, value] of new FormData(event.currentTarget)) if (typeof value === 'string' && value) query.set(name, value)
      visit(route + (query.size ? '?' + query : ''))
    }}>
      {p.Group && <input type="hidden" name="group" value={p.Group} />}
      {episodes && <>
        <Field label={p.SinceLabel}><input type="datetime-local" step={1} name="since" required defaultValue={p.Since} /></Field>
        <Field label={p.UntilLabel}><input type="datetime-local" step={1} name="until" required defaultValue={p.Until} /></Field>
      </>}
      {p.Filters?.map(filter => <Field key={filter.Name} label={filter.Label}><Select name={filter.Name} options={filter.Options} /></Field>)}
      <button className="button primary">{l.Apply}</button>
      {(p.Reset || p.AllGroups) && <Link className="button" href={p.Reset || p.AllGroups}>{p.Reset ? l.Reset : l.AllGroups}</Link>}
    </form>
    {episodes && p.Coverage && <p className="notice" role="status">{p.Coverage}</p>}
    <Table columns={[l.Incident, l.Started, episodes ? p.Status : l.Resolved, l.Evidence]} empty={p.Empty || l.Empty}
      rows={rows.map(row => [
        <><Link href={row.Href}>{row.Title}</Link><small>{row.Service || row.Services}</small>
          <ToneText text={row.Severity} tone={row.SeverityTone} /><small>{row.Exercise ? l.Exercise : [row.Origin, row.OpeningUnknown].filter(Boolean).join(' · ')}</small></>,
        row.Started, <>{row.State && <ToneText text={row.State} tone={row.Tone} />}{row.Resolved && <div>{row.Resolved}</div>}</>,
        episodes ? <>{row.Wake}<small>{row.Programmes?.map(run => [run.Title, run.Status, run.Exit].filter(Boolean).join(' · ')).join('; ') || l.NoProgramme}</small>
          <small>{row.Human}</small><small>{row.Passing}</small><small>{row.Participants?.join(', ')}</small></>
          : <><div>{row.Verification || l.NoVerification}</div><small>{row.AgentReport ? l.AgentReport : l.NoAgentReport}</small>
            <small>{row.HumanAck ? l.HumanAck : l.NoHumanAck}</small><small>{row.AgentPassingCheck ? l.AgentPassingCheck : row.PassingCheck ? l.CheckRecorded : l.NoPassingCheck}</small>
            <small>{row.Participants?.join(', ')}</small></>,
      ])} />
    {(p.Before || p.First || p.Older) && <div className="resource-tools">
      {p.First && <Link className="button" href={p.First}>{l.First}</Link>}
      {(p.Before || p.Older) && <Link className="button" href={p.Before || p.Older}>{l.Older}</Link>}
    </div>}
  </>
}

type Programme = { Title: string; Status: string; Tone: Tone; Exit: string }
type Outcome = {
  L: Text; Incident: HistoryRow & { ExerciseRun: string; Duration: string }; Audit: string; Alert: string; Group: string
  Latest?: Text; PassingCheck?: Text; HumanAck?: Text; Participation: Text[] | null; Sessions: SessionRecord[] | null
  Deliveries: { Hook: string; Event: string; Status: string; Tone: Tone; Delivery: Text; Started: Text; Report: Text; Run: Programme | null }[] | null
}

export function Outcome({ data: p }: { data: Outcome }) {
  const l = p.L
  const i = p.Incident
  return <>
    <div className="resource-tools">{p.Alert && <Link className="button" href={p.Alert}>{l.Alert}</Link>}
      {p.Audit && <Link className="button" href={p.Audit}>{l.Audit}</Link>}</div>
    <Facts rows={[
      { K: l.Service, V: i.Service }, { K: l.Severity, V: i.Severity }, { K: l.Started, V: i.Started },
      { K: l.Resolved, V: [i.Resolved, i.Duration].filter(Boolean).join(' · ') }, { K: l.Group, V: p.Group },
      { K: l.ExerciseRun, V: i.ExerciseRun }, { K: l.Acknowledgement, V: p.HumanAck ? [p.HumanAck.By, p.HumanAck.At, p.HumanAck.Via].filter(Boolean).join(' · ') : '' },
    ].filter(row => row.V)} />
    <Section title={l.Verification}>
      <Facts rows={[{ K: l.Verification, V: p.Latest ? [p.Latest.Summary, p.Latest.By, p.Latest.At].join(' · ') : l.NoVerification },
        { K: l.PassingCheck, V: p.PassingCheck ? [p.PassingCheck.Summary, p.PassingCheck.By, p.PassingCheck.At, p.PassingCheck.Phase].filter(Boolean).join(' · ') : l.NoPassingCheck }]} />
    </Section>
    <Section title={l.Participation}><Table columns={[l.Participation, l.Evidence, l.Started]} empty={l.NoParticipants}
      rows={(p.Participation ?? []).map(row => [<>{row.By}<small>{row.Kind} {row.Via}</small></>, row.Action, row.At])} /></Section>
    {p.Deliveries?.length ? <Section title={l.Deliveries}>
      <Table columns={[l.Delivery, l.Programme, l.ProgrammeStart, l.ProgrammeReport]} rows={p.Deliveries.map(row => [
        <>{row.Hook}<small>{row.Event} · {row.Delivery.Text}</small><ToneText text={row.Status} tone={row.Tone} /></>,
        row.Run ? <><ToneText text={row.Run.Status} tone={row.Run.Tone} /><small>{row.Run.Exit}</small></> : l.NoProgramme,
        row.Started.Text, row.Report.Text,
      ])} />
    </Section> : null}
    {p.Sessions?.length ? <Section title={l.Sessions}><SessionRows rows={p.Sessions} l={l} details={false} /></Section> : null}
  </>
}
