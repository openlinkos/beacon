import { Action, Facts, Field, Section, Table, ToneText } from './admin'
import type { Pick } from './admin'
import type { Tone } from './api'
import { Link } from './navigation'

type Text = Record<string, string>
type Ref = { Label: string; Href: string }
const join = (values?: string[] | null) => values?.join(', ') || ''
const link = (href: string, label: string, primary = false) =>
  href && <Link className={primary ? 'button primary' : 'button'} href={href}>{label}</Link>

type StatusPagesData = {
  L: Text; Create: string; CreateLabel: string; DefaultHelp: string
  Rows: ({ Tone: Tone } & Record<string, string>)[] | null
}

export function StatusPages({ data: p }: { data: StatusPagesData }) {
  const l = p.L
  return <>
    {p.Create ? <div className="resource-tools">{link(p.Create, p.CreateLabel, true)}</div> : p.DefaultHelp && <p role="status">{p.DefaultHelp}</p>}
    <Table columns={[l.Page, l.State, l.Components, l.Incidents, l.Maintenance, l.Languages]}
      empty={l.NoPages} rows={(p.Rows ?? []).map(row => [
        <><Link href={row.Href}>{row.Title}</Link><small><a href={row.URL}>{row.URL}</a></small></>,
        <ToneText text={row.State} tone={row.Tone} />, row.Components, row.Incidents, row.Maintenance, row.Languages,
      ])} />
  </>
}

type StatusDetail = {
  L: Text; URL: string; Open: string; JSON: string; Feed: string; Logo: string; AddComponent: string
  Components: { Name: string; Title: string; State: string; Tone: Tone; Availability: string; Services: Ref[] | null; Edit: string; Remove: string }[] | null
  Incidents: { Title: string; Href: string; Impact: string; ImpactTone: Tone; Status: string; StatusTone: Tone; Started: string; Resolved: string }[] | null
  Maintenance: { Title: string; Status: string; Starts: string; Ends: string; Components: string }[] | null
}

export function StatusDetail({ data: p }: { data: StatusDetail }) {
  const l = p.L
  return <>
    <div className="resource-tools">{link(p.Open, l.Open, true)}{link(p.JSON, l.JSON)}{link(p.Feed, l.Feed)}{link(p.Logo, l.Badge)}</div>
    <Section title={l.Components} actions={link(p.AddComponent, l.AddComponent)}>
      <Table columns={[l.Component, l.State, l.Availability, l.Services, '']} empty={l.None} rows={(p.Components ?? []).map(row => [
        row.Title, <ToneText text={row.State} tone={row.Tone} />, row.Availability,
        <ul>{row.Services?.map(service => <li key={service.Href}><Link href={service.Href}>{service.Label}</Link></li>)}</ul>,
        <div className="resource-tools">{link(row.Edit, l.EditServices)}{link(row.Remove, l.RemoveComponent)}</div>,
      ])} />
    </Section>
    <Section title={l.Incidents}><Table columns={[l.Title, l.Impact, l.Status, l.Started, l.Resolved]} empty={l.None}
      rows={(p.Incidents ?? []).map(row => [<Link href={row.Href}>{row.Title}</Link>, <ToneText text={row.Impact} tone={row.ImpactTone} />,
        <ToneText text={row.Status} tone={row.StatusTone} />, row.Started, row.Resolved])} /></Section>
    <Section title={l.Maintenance}><Table columns={[l.Title, l.Status, l.Starts, l.Ends, l.Components]} empty={l.None}
      rows={(p.Maintenance ?? []).map(row => [row.Title, row.Status, row.Starts, row.Ends, row.Components])} /></Section>
  </>
}

type WindowRow = { Title: string; Description: string; Status: string; Tone: Tone; Services: string; Starts: string; Ends: string; By: string; Via: string; End: string; EndLabel: string }
type SilenceRow = { Matches: string; Alert: string; AlertHref: string; Tone: Tone; State: string; Left: string; Comment: string; Starts: string; Ends: string; By: string; End: string; Window: string }
type SilencesData = { L: Text; Schedule: string; Unread: string; Windows: WindowRow[] | null; Silences: SilenceRow[] | null }

export function Silences({ data: p }: { data: SilencesData }) {
  const l = p.L
  return <>
    {p.Unread && <p className="notice" role="alert">{p.Unread}</p>}
    <div id="maintenance"><Section title={l.Windows} actions={link(p.Schedule, l.Schedule, true)}>
      <Table columns={[l.Title, l.Status, l.Services, l.Starts, l.Ends, l.By, '']} empty={l.NoWindows}
        rows={(p.Windows ?? []).map(row => [
          <>{row.Title}{row.Description && <small>{row.Description}</small>}</>, <ToneText text={row.Status} tone={row.Tone} />,
          row.Services, row.Starts, row.Ends, <>{row.By}<small>{row.Via}</small></>,
          row.End && <Action action={row.End} label={row.EndLabel} compact />,
        ])} />
    </Section></div>
    <div id="silences"><Section title={l.Silences}>
      <Table columns={[l.Matches, l.State, l.Comment, l.Starts, l.Ends, l.By, '']} empty={l.NoSilences}
        rows={(p.Silences ?? []).map(row => [
          <><code>{row.Matches}</code>{row.Alert && <small><Link href={row.AlertHref}>{row.Alert}</Link></small>}</>,
          <><ToneText text={row.State} tone={row.Tone} /><small>{row.Left}</small></>, row.Comment, row.Starts, row.Ends, row.By,
          row.End ? <Action action={row.End} label={l.End} compact /> : row.Window,
        ])} />
    </Section></div>
  </>
}

function ServiceInputs({ label, options, values }: { label?: string; options?: Pick[] | null; values?: string[] | null }) {
  if (values) return values.map(value => <input key={value} type="hidden" name="service" value={value} />)
  if (!options?.length) return null
  return <fieldset className="option-group"><legend>{label}</legend>
    {options.map(service => <label key={service.Value}><input name="service" type="checkbox" value={service.Value} defaultChecked={service.Selected} />{service.Label}</label>)}
  </fieldset>
}

type MaintenanceForm = {
  L: Text; Form: string; Silences: string; Monitors: string; Version: number; Services: Pick[] | null
  Draft: { Title: string; Description: string; Start: string; End: string; Services: string[] }
  Preview: { Title: string; Description: string; Starts: string; Ends: string; Pages: Ref[] | null } | null
}

export function Maintenance({ data: p }: { data: MaintenanceForm }) {
  const l = p.L
  const d = p.Draft
  if (p.Preview) return <Section title={l.Effect}>
    <Facts rows={[
      { K: l.Title, V: p.Preview.Title }, { K: l.Description, V: p.Preview.Description },
      { K: l.Services, V: join(d.Services) }, { K: l.Starts, V: p.Preview.Starts }, { K: l.Ends, V: p.Preview.Ends },
    ].filter(row => row.V)} />
    <h3>{l.Public}</h3>
    {p.Preview.Pages?.length ? <ul>{p.Preview.Pages.map(page => <li key={page.Href}><Link href={page.Href}>{page.Label}</Link></li>)}</ul> : <p>{l.NoPublic}</p>}
    <Action key="review" action={p.Form} intent="schedule" edit={l.Edit} label={l.Publish} primary
      hidden={{ version: p.Version, title: d.Title, description: d.Description, start: d.Start, end: d.End }}>
      <ServiceInputs values={d.Services} />
    </Action>
  </Section>
  return <>
    {p.Services?.length ? <Action key="draft" action={p.Form} label={l.Preview} intent="preview" primary>
      <Field label={l.Title}><input name="title" required maxLength={200} defaultValue={d.Title} /></Field>
      <Field label={l.Description}><textarea name="description" rows={3} maxLength={2000} defaultValue={d.Description} /></Field>
      <ServiceInputs options={p.Services} label={l.Services} />
      <div className="form-grid">
        <Field label={l.Starts}><input name="start" type="datetime-local" defaultValue={d.Start} /></Field>
        <Field label={l.Ends}><input name="end" type="datetime-local" defaultValue={d.End} required /></Field>
      </div>
      <small>{l.Timezone}</small>
    </Action> : <p>{l.NoServices} <Link href={p.Monitors}>{l.Monitors}</Link></p>}
    <div className="resource-tools">{link(p.Silences, l.Cancel)}</div>
  </>
}

type PublicationPreview = {
  Title: string; Confirmation: string; Visible: string[] | null; Added: string[] | null; Removed: string[] | null; AllNotes: boolean
  NotesHeading: string; MaintenanceHeading: string
  Notes: { Title: string; Text: string; Time: string }[] | null
  Announcements: { Title: string; Description: string; Starts: string; Ends: string; Status: string }[] | null
}
type PublicationForm = {
  L: Text; Form: string; Back?: string; Page?: string; Mode?: string; StateVersion?: number; Version?: number
  Name: string; PublicTitle?: string; ComponentTitle?: string; Monitors?: string; URL?: string; PublicURL?: string
  Services?: Pick[] | null; Choices?: Pick[] | null; Automatic?: string[] | null; Preview: PublicationPreview | null
}

export function Publication({ data: p }: { data: PublicationForm }) {
  const l = p.L
  const create = !p.Mode
  const nameField = create ? 'name' : 'component'
  const title = p.PublicTitle ?? p.ComponentTitle
  const options = p.Services ?? p.Choices
  const version = p.StateVersion ?? p.Version
  const destination = p.URL || p.PublicURL
  const editName = create || p.Mode === 'add'
  const remove = p.Mode === 'remove'
  if (p.Preview) {
    const preview = p.Preview
    const values = create ? preview.Visible : options?.filter(option => option.Selected).map(option => option.Value)
    return <Section title={l.PublicEffect}>
      <Facts rows={[
        { K: l.Title, V: preview.Title || title || '' },
        { K: l.Destination, V: destination || '', Href: destination },
        { K: l.Added, V: join(preview.Added) }, { K: l.Removed, V: join(preview.Removed) },
        { K: l.Visible || l.Services, V: join(preview.Visible) || l.NoServices }, { K: l.Automatic, V: join(p.Automatic) },
      ].filter(row => row.V)} />
      {preview.AllNotes && l.AllNotes && <p role="alert">{l.AllNotes}</p>}
      {preview.Notes?.length ? <><h3>{preview.NotesHeading}</h3><ul className="plain-list">{preview.Notes.map((note, index) =>
        <li key={index}><strong>{note.Title}</strong> <time>{note.Time}</time><p>{note.Text}</p></li>)}</ul></> : null}
      {preview.Announcements?.length ? <><h3>{preview.MaintenanceHeading}</h3><ul className="plain-list">
        {preview.Announcements.map((window, index) => <li key={index}><strong>{window.Title}</strong> {window.Status}
          <div>{window.Starts} – {window.Ends}</div>{window.Description && <p>{window.Description}</p>}</li>)}</ul></> : null}
      <Action key="review" action={p.Form} label={remove ? l.Remove : l.Publish} intent="publish" edit={l.Edit} danger={remove} primary={!remove}
        hidden={{ version, [nameField]: p.Name, title: editName ? title : undefined, confirmation: preview.Confirmation }}>
        {!remove && <ServiceInputs values={values ?? []} />}
      </Action>
    </Section>
  }
  return <>
    {create && !options?.length ? <p>{l.NoServices} {p.Monitors && <Link href={p.Monitors}>{l.Monitors}</Link>}</p> :
      <Action key="draft" action={p.Form} label={l.Preview} intent="preview" primary
        hidden={{ version, [nameField]: editName ? undefined : p.Name }}>
        {editName ? <>
          <Field label={l.Name}><input name={nameField} defaultValue={p.Name} required maxLength={63} pattern="[a-z0-9][a-z0-9\-]{0,62}" /></Field>
          <Field label={l.Title}><input name="title" defaultValue={title} required maxLength={120} /></Field>
        </> : <Facts rows={[{ K: l.Title, V: title || p.Name }]} />}
        {remove ? <p>{l.RemoveHint}</p> : <ServiceInputs label={l.Services} options={options} />}
        {p.Automatic?.length ? <Facts rows={[{ K: l.Automatic, V: join(p.Automatic) }]} /> : null}
      </Action>}
    {(p.Back || p.Page) && <div className="resource-tools">{link(p.Back || p.Page || '', l.Cancel)}</div>}
  </>
}
