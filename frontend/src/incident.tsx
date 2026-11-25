import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Action, AdminContext, Field, visit } from './admin'
import type { Incident as IncidentData, Label } from './api'
import { APIError, incidentAction } from './api'
import { Empty, Loading, PageHeading, Signal } from './components'
import { Link } from './navigation'
import { useResource } from './resources'

type ActiveIncident = Exclude<IncidentData, { archived: string }>

function Response({ data, t, csrf, reload }: { data: ActiveIncident; t: Label; csrf: string; reload: () => void }) {
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [bad, setBad] = useState(false)
  const detail = data.Detail
  const l = detail.Labels
  const forms = detail.Actions

  async function apply(action: 'ack' | 'notes' | 'probe-now', text?: string) {
    setBusy(true)
    setMessage('')
    setBad(false)
    try {
      await incidentAction(data.ID, action, csrf, { episode: data.Episode, ackVersion: data.Actions.AckVersion, text })
      setMessage(t('saved'))
      reload()
      return true
    } catch (error) {
      setBad(true)
      setMessage(t(error instanceof APIError && error.status === 409 ? 'changed' : 'failed'))
      return false
    } finally {
      setBusy(false)
    }
  }

  async function note(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = event.currentTarget
    const text = new FormData(form).get('text')
    if (typeof text === 'string' && await apply('notes', text)) form.reset()
  }

  return <aside className="response" aria-labelledby="response">
    <h2 id="response">{t('response')}</h2>
    <p className="ownership">{data.Ownership || data.Status}</p>
    <p>{data.When}</p>
    {data.Escalation && <p>{data.Escalation}</p>}
    {data.Phone && <a href="#timeline">{data.Phone}</a>}
    {detail.PhoneCancellation && <div><Signal tone={detail.PhonePending ? 'warning' : ''}>{detail.PhoneCancellation}</Signal>
      {detail.PhonePending && <button className="button" type="button" onClick={reload}>{l.RefreshStatus}</button>}</div>}
    {detail.Feedback && <div role={detail.FeedbackTone === 'danger' ? 'alert' : 'status'} data-tone={detail.FeedbackTone}>{detail.Feedback}</div>}
    <div className="response-buttons">
      {data.Actions.CanAck && <button className="button primary" type="button" disabled={busy} onClick={() => void apply('ack')}>
        {t(data.Actions.TakeOver ? 'takeOver' : 'ack')}
      </button>}
      {data.Actions.CanProbe && <button className="button" type="button" disabled={busy} onClick={() => void apply('probe-now')}>
        {t('probeNow')}
      </button>}
    </div>
    {data.Actions.CanAck && <details className="response-section note-disclosure"><summary>{l.AckNote}</summary>
      <form className="note-form" onSubmit={event => {
        event.preventDefault()
        const text = new FormData(event.currentTarget).get('text')
        if (typeof text === 'string') void apply('ack', text)
      }}>
        <Field label={t('note')}><textarea name="text" rows={3} maxLength={2000} required /></Field>
        <button className="button" disabled={busy}>{data.Actions.TakeOver ? l.TakeOverWithNote : l.AckWithNote}</button>
      </form>
    </details>}
    {busy && <p role="status">{t('loading')}</p>}
    {message && <p role={bad ? 'alert' : 'status'} className={bad ? 'operation-error' : 'operation-result'}>{message}</p>}
    {data.LatestCheck && <div className="response-section"><h3>{t('latestCheck')}</h3><p>{data.LatestCheck.Summary}</p>
      {detail.LatestProbeHref && <Link href={detail.LatestProbeHref}>{l.ViewCheck}</Link>}</div>}
    {data.Machines && <div className="response-section">
      <h3>{data.Machines.Title}</h3>
      <ul>{data.Machines.Items?.map(item => <li key={item.Href}><Link href={item.Href}>{item.V}</Link></li>)}</ul>
      {!data.Machines.Items?.length && data.Machines.Hint && <p>{data.Machines.Hint}</p>}
    </div>}
    {(data.Delivery || data.Hook) && <div className="response-section">
      {data.Delivery && <p>{data.Delivery}</p>}
      {data.Hook && <p>{data.Hook}</p>}
    </div>}
    {detail.AgentRun && <Link href={'#' + (detail.AgentRun.Anchor || 'timeline')}>{detail.AgentRun.Title} {detail.AgentRun.Status}</Link>}
    {detail.Handover.Preview && <div className="response-section"><h3>{l.AcknowledgementNote}</h3><p>{detail.Handover.Preview}</p>
      {detail.Handover.Full && <details><summary>{l.FullAcknowledgementNote}</summary><p>{detail.Handover.Full}</p></details>}
    </div>}
    {data.Actions.CanNote && <details className="response-section note-disclosure">
      <summary>{t('addNote')}</summary>
      <form className="note-form" onSubmit={note}>
        <label htmlFor="incident-note">{t('note')}</label>
        <textarea id="incident-note" name="text" rows={3} maxLength={2000} required />
        <button className="button" type="submit" disabled={busy}>{t('addNote')}</button>
      </form>
    </details>}
    {forms?.Silence && <details className="response-section note-disclosure"><summary>{l.Silence}</summary>
      <Action action={forms.Silence} label={l.Silence} hidden={{ expected_episode: forms.Episode, return_episode: forms.ReturnEpisode }}>
        <Field label={l.SilenceWhy}><input name="comment" maxLength={500} required /></Field>
        <Field label={l.Duration}><select name="duration">{forms.Durations?.map(item => <option key={item.K} value={item.K}>{item.V}</option>)}</select></Field>
      </Action>
    </details>}
    {forms?.Unsilence && <div className="response-section"><Action action={forms.Unsilence} label={l.EndSilence} compact hidden={{ back: forms.Back }} /></div>}
  </aside>
}

export function Incident({ path, hash, t, csrf }: { path: string; hash: string; t: Label; csrf: string }) {
  const [revision, setRevision] = useState(0)
  const resource = useResource<IncidentData>(path, revision)
  const data = resource.data
  useEffect(() => {
    if (data && /^#[a-z\d_-]+$/i.test(hash)) document.getElementById(hash.slice(1))?.scrollIntoView()
  }, [data, hash])
  if (resource.error) return <Empty>{t('loadError')}</Empty>
  if (!data) return <Loading t={t} />
  if ('archived' in data) return <Link href={data.archived}>{t('openHistory')}</Link>
  const detail = data.Detail
  const l = detail.Labels
  return <AdminContext.Provider value={{ csrf, t, finish: result => {
    if (result?.Redirect) visit(result.Redirect)
    setRevision(value => value + 1)
  } }}>
    <PageHeading title={data.Title} actions={detail.Share && <Link className="button" href={detail.Share}>{l.ShareLinks}</Link>} />
    <div className="incident-meta"><Signal tone={data.StatusTone}>{data.Status}</Signal><Signal tone={data.SeverityTone}>{data.Severity}</Signal>
      {detail.Services?.length ? <span>{detail.Services.join(', ')}</span> : null}</div>
    <div className="incident-layout">
      <Response data={data} t={t} csrf={csrf} reload={() => setRevision(value => value + 1)} />
      <div className="incident-evidence">
        <nav className="evidence-nav" aria-label={t('timeline')}>
          <a href="#timeline">{t('timeline')}</a><a href="#probes">{t('probes')}</a>
          <a href="#sessions">{t('sessions')}</a><a href="#notes">{t('notes')}</a>
        </nav>
        {data.Description && <p className="incident-description">{data.Description}</p>}
        <section id="timeline" className="evidence-section"><h2>{t('timeline')}</h2>
          {data.Events?.length ? <ol className="timeline">{data.Events.map((row, index) =>
            <li key={`${row.ISO}-${index}`} id={row.Anchor || undefined}>
              <time dateTime={row.ISO}>{row.At}</time>
              <div>
                <p>{row.Text} {row.Href && <Link href={row.Href}>{row.HrefText}</Link>}</p>
                {row.Detail && <p className="event-detail">{row.Detail}</p>}
                {row.Probe && <details open={row.Probe.Open || undefined}><summary>{row.Probe.Summary}</summary>
                  {row.Probe.RecoveryHint && <p>{row.Probe.RecoveryHint}</p>}
                  {row.Probe.Rows?.map((probe, i) => <p key={`${probe.Location}-${i}`}>{probe.Location} <Signal tone={probe.Tone}>{probe.State}</Signal> {probe.Reason}</p>)}
                </details>}
              </div>
            </li>)}</ol> : <Empty>{t('none')}</Empty>}
        </section>
        <section id="probes" className="evidence-section"><h2>{t('probes')}</h2>
          {data.Probes?.length ? <ul className="evidence-list">{data.Probes.map((probe, index) =>
            <li key={`${probe.Instance}-${index}`}><strong>{probe.Location}</strong> <Signal tone={probe.Tone}>{probe.State}</Signal>
              <p>{probe.Instance}</p><p>{probe.Reason || probe.Detail}</p>
            </li>)}</ul> : <Empty>{data.ProbesError || t('none')}</Empty>}
          <small>{data.Window}</small>
        </section>
        <section id="sessions" className="evidence-section"><h2>{t('sessions')}</h2>
          {data.Sessions?.length ? <ul className="evidence-list">{data.Sessions.map(session =>
            <li key={session.ID}><Link href={session.Href}>{session.Agent} / {session.Machine}</Link><p>{session.Started} {session.Kind}</p></li>)}</ul> : <Empty>{t('none')}</Empty>}
        </section>
        <section id="notes" className="evidence-section"><h2>{t('notes')}</h2>
          {data.Notes?.length ? <ul className="evidence-list">{data.Notes.map((note, index) =>
            <li key={`${note.ISO}-${index}`}><strong>{note.By}</strong> <time dateTime={note.ISO}>{note.At}</time><p>{note.Text}</p></li>
          )}</ul> : <Empty>{t('none')}</Empty>}
        </section>
        {detail.Related?.length ? <section className="evidence-section"><h2>{l.Related}</h2>
          <ul className="evidence-list">{detail.Related.map(row => <li key={row.Href}><Link href={row.Href}>{row.Title}</Link> <Signal tone={row.SeverityClass}>{row.Severity}</Signal></li>)}</ul>
        </section> : null}
        {detail.Changes?.length ? <section className="evidence-section"><h2>{l.Changes}</h2>
          <ul className="evidence-list">{detail.Changes.map((row, index) => <li key={index}>
            {row.Href ? <a href={row.Href}>{row.Summary}</a> : <strong>{row.Summary}</strong>}<p>{row.At} · {row.Source} · {row.About}</p>
          </li>)}</ul>
        </section> : null}
        {(detail.Channel || detail.Agents?.length || detail.Runbook || detail.Dashboard) && <section className="evidence-section"><h2>{l.Details}</h2>
          <div className="resource-tools">
            {detail.Runbook && <a className="button" href={detail.Runbook}>{l.Runbook}</a>}
            {detail.Dashboard && <a className="button" href={detail.Dashboard}>{l.Dashboard}</a>}
            {detail.Channel && <Signal tone={detail.Paused ? 'danger' : 'success'}>{detail.Channel}</Signal>}
          </div>
          {detail.Agents?.length ? <ul>{detail.Agents.map(agent => <li key={agent}>{agent}</li>)}</ul> : null}
        </section>}
      </div>
    </div>
  </AdminContext.Provider>
}
