import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Action, AdminContext, Field, visit } from './admin'
import type { Incident as IncidentData, Label } from './api'
import { APIError, incidentAction } from './api'
import { Empty, Loading, PageHeading, Signal } from './components'
import { Icon } from './icons'
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

  return (
    <aside className="response" aria-labelledby="response">
      <div className="response-head">
        <h2 id="response">{t('response')}</h2>
        <span className="response-when">{data.When}</span>
      </div>
      <p className="ownership">{data.Ownership || data.Status}</p>
      {data.Escalation && <p className="response-escalation">{data.Escalation}</p>}
      {data.Phone && <a className="response-phone" href="#timeline">{data.Phone}</a>}
      {detail.PhoneCancellation && (
        <div className="response-cancel">
          <Signal tone={detail.PhonePending ? 'warning' : ''}>{detail.PhoneCancellation}</Signal>
          {detail.PhonePending && (
            <button className="button" type="button" onClick={reload}>
              <Icon name="refresh-cw" size="xs" />
              <span>{l.RefreshStatus}</span>
            </button>
          )}
        </div>
      )}
      {detail.Feedback && (
        <div className="notice" role={detail.FeedbackTone === 'danger' ? 'alert' : 'status'} data-tone={detail.FeedbackTone}>
          {detail.Feedback}
        </div>
      )}
      {(data.Actions.CanAck || data.Actions.CanProbe) && (
        <div className="response-buttons">
          {data.Actions.CanAck && (
            <button className="button primary" type="button" disabled={busy} onClick={() => void apply('ack')}>
              <Icon name="check" size="xs" />
              <span>{t(data.Actions.TakeOver ? 'takeOver' : 'ack')}</span>
            </button>
          )}
          {data.Actions.CanProbe && (
            <button className="button" type="button" disabled={busy} onClick={() => void apply('probe-now')}>
              <Icon name="refresh-cw" size="xs" />
              <span>{t('probeNow')}</span>
            </button>
          )}
        </div>
      )}
      {data.Actions.CanAck && (
        <details className="response-section note-disclosure">
          <summary>{l.AckNote}</summary>
          <form
            className="note-form"
            onSubmit={event => {
              event.preventDefault()
              const text = new FormData(event.currentTarget).get('text')
              if (typeof text === 'string') void apply('ack', text)
            }}
          >
            <Field label={t('note')}><textarea name="text" rows={3} maxLength={2000} required /></Field>
            <button className="button primary" disabled={busy}>{data.Actions.TakeOver ? l.TakeOverWithNote : l.AckWithNote}</button>
          </form>
        </details>
      )}
      {busy && <p className="response-status" role="status">{t('loading')}</p>}
      {message && <p role={bad ? 'alert' : 'status'} className={bad ? 'operation-error' : 'operation-result'}>{message}</p>}

      {data.LatestCheck && (
        <div className="response-section">
          <h3>{t('latestCheck')}</h3>
          <p>{data.LatestCheck.Summary}</p>
          {detail.LatestProbeHref && <Link className="text-action" href={detail.LatestProbeHref}>{l.ViewCheck}</Link>}
        </div>
      )}

      {data.Machines && (
        <div className="response-section">
          <h3>{data.Machines.Title}</h3>
          {data.Machines.Items?.length ? (
            <ul className="machine-chips">
              {data.Machines.Items.map(item => (
                <li key={item.Href}>
                  <Link href={item.Href}>
                    <Icon name="server" size="xs" />
                    <span>{item.V}</span>
                  </Link>
                </li>
              ))}
            </ul>
          ) : null}
          {!data.Machines.Items?.length && data.Machines.Hint && <p>{data.Machines.Hint}</p>}
        </div>
      )}

      {(data.Delivery || data.Hook || detail.AgentRun) && (
        <div className="response-section">
          {data.Delivery && <p>{data.Delivery}</p>}
          {data.Hook && <p>{data.Hook}</p>}
          {detail.AgentRun && (
            <Link className="agent-run-link" href={'#' + (detail.AgentRun.Anchor || 'timeline')}>
              <Icon name="bot" size="xs" />
              <span>{detail.AgentRun.Title}</span>
              <Signal tone={detail.AgentRun.Tone}>{detail.AgentRun.Status}</Signal>
            </Link>
          )}
        </div>
      )}

      {detail.Handover.Preview && (
        <div className="response-section">
          <h3>{l.AcknowledgementNote}</h3>
          <p>{detail.Handover.Preview}</p>
          {detail.Handover.Full && (
            <details><summary>{l.FullAcknowledgementNote}</summary><p>{detail.Handover.Full}</p></details>
          )}
        </div>
      )}

      {data.Actions.CanNote && (
        <details className="response-section note-disclosure">
          <summary>{t('addNote')}</summary>
          <form className="note-form" onSubmit={note}>
            <label htmlFor="incident-note">{t('note')}</label>
            <textarea id="incident-note" name="text" rows={3} maxLength={2000} required />
            <button className="button primary" type="submit" disabled={busy}>{t('addNote')}</button>
          </form>
        </details>
      )}

      {forms?.Silence && (
        <details className="response-section note-disclosure">
          <summary>{l.Silence}</summary>
          <Action action={forms.Silence} label={l.Silence} hidden={{ expected_episode: forms.Episode, return_episode: forms.ReturnEpisode }}>
            <Field label={l.SilenceWhy}><input name="comment" maxLength={500} required /></Field>
            <Field label={l.Duration}>
              <select name="duration">{forms.Durations?.map(item => <option key={item.K} value={item.K}>{item.V}</option>)}</select>
            </Field>
          </Action>
        </details>
      )}

      {forms?.Unsilence && (
        <div className="response-section">
          <Action action={forms.Unsilence} label={l.EndSilence} compact hidden={{ back: forms.Back }} />
        </div>
      )}
    </aside>
  )
}

export function Incident({ path, hash, t, csrf }: { path: string; hash: string; t: Label; csrf: string }) {
  const [revision, setRevision] = useState(0)
  const resource = useResource<IncidentData>(path, revision)
  const data = resource.data

  useEffect(() => {
    if (data && /^#[a-z\d_-]+$/i.test(hash)) document.getElementById(hash.slice(1))?.scrollIntoView()
  }, [data, hash])

  if (resource.error) return <Empty icon="triangle-alert">{t('loadError')}</Empty>
  if (!data) return <Loading t={t} />
  if ('archived' in data) return <Link className="button" href={data.archived}>{t('openHistory')}</Link>

  const detail = data.Detail
  const l = detail.Labels

  return (
    <AdminContext.Provider value={{
      csrf,
      t,
      finish: result => {
        if (result?.Redirect) visit(result.Redirect)
        setRevision(value => value + 1)
      },
    }}>
      <PageHeading
        title={data.Title}
        actions={detail.Share && (
          <Link className="button" href={detail.Share}>
            <Icon name="external-link" size="xs" />
            <span>{l.ShareLinks}</span>
          </Link>
        )}
      />

      <div className="incident-meta">
        <Signal tone={data.StatusTone} pulse={data.StatusTone === 'danger'}>{data.Status}</Signal>
        <Signal tone={data.SeverityTone}>{data.Severity}</Signal>
        {detail.Services?.map(service => <span key={service} className="mono-chip">{service}</span>)}
      </div>

      <div className="incident-layout">
        <Response data={data} t={t} csrf={csrf} reload={() => setRevision(value => value + 1)} />

        <div className="incident-evidence">
          <nav className="evidence-nav" aria-label={t('timeline')}>
            <a href="#timeline">{t('timeline')}</a>
            <a href="#probes">{t('probes')}</a>
            <a href="#sessions">{t('sessions')}</a>
            <a href="#notes">{t('notes')}</a>
          </nav>

          {data.Description && <p className="incident-description">{data.Description}</p>}

          <section id="timeline" className="evidence-section">
            <h2>{t('timeline')}</h2>
            {data.Events?.length ? (
              <ol className="timeline">
                {data.Events.map((row, index) => (
                  <li key={`${row.ISO}-${index}`} id={row.Anchor || undefined}>
                    <time dateTime={row.ISO}>{row.At}</time>
                    <div className="timeline-body">
                      <p>{row.Text} {row.Href && <Link href={row.Href}>{row.HrefText}</Link>}</p>
                      {row.Detail && <p className="event-detail">{row.Detail}</p>}
                      {row.Probe && (
                        <details open={row.Probe.Open || undefined}>
                          <summary>{row.Probe.Summary}</summary>
                          {row.Probe.RecoveryHint && <p>{row.Probe.RecoveryHint}</p>}
                          <div className="probe-inline-list">
                            {row.Probe.Rows?.map((probe, i) => (
                              <div className="probe-inline-row" key={`${probe.Location}-${i}`}>
                                <strong>{probe.Location}</strong>
                                <Signal tone={probe.Tone}>{probe.State}</Signal>
                                <span>{probe.Reason}</span>
                              </div>
                            ))}
                          </div>
                        </details>
                      )}
                    </div>
                  </li>
                ))}
              </ol>
            ) : <Empty icon="clock">{t('none')}</Empty>}
          </section>

          <section id="probes" className="evidence-section">
            <div className="evidence-head">
              <h2>{t('probes')}</h2>
              {data.Window && <small>{data.Window}</small>}
            </div>
            {data.Probes?.length ? (
              <div className="table-scroll">
                <table className="resource-table">
                  <thead>
                    <tr>
                      <th scope="col">{t('location')}</th>
                      <th scope="col">{t('result')}</th>
                      <th scope="col">{t('target')}</th>
                      <th scope="col">{t('reason')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.Probes.map((probe, index) => (
                      <tr key={`${probe.Instance}-${index}`}>
                        <td><strong>{probe.Location}</strong></td>
                        <td data-label={t('result')}><Signal tone={probe.Tone}>{probe.State}</Signal></td>
                        <td data-label={t('target')}><code>{probe.Instance}</code></td>
                        <td data-label={t('reason')}>{probe.Reason || probe.Detail || '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : <Empty icon="globe">{data.ProbesError || t('none')}</Empty>}
          </section>

          <section id="sessions" className="evidence-section">
            <h2>{t('sessions')}</h2>
            {data.Sessions?.length ? (
              <ul className="evidence-list">
                {data.Sessions.map(session => (
                  <li key={session.ID} className="evidence-row">
                    <div>
                      <Link href={session.Href}><strong>{session.Agent}</strong> / {session.Machine}</Link>
                      <p>{session.Started} · {session.Kind}</p>
                    </div>
                    <Link className="button" href={session.Href}>
                      <Icon name="terminal" size="xs" />
                      <span>{t('replay')}</span>
                    </Link>
                  </li>
                ))}
              </ul>
            ) : <Empty icon="terminal">{t('none')}</Empty>}
          </section>

          <section id="notes" className="evidence-section">
            <h2>{t('notes')}</h2>
            {data.Notes?.length ? (
              <ul className="evidence-list">
                {data.Notes.map((note, index) => (
                  <li key={`${note.ISO}-${index}`}>
                    <div className="note-head">
                      <strong>{note.By}</strong>
                      <time dateTime={note.ISO}>{note.At}</time>
                    </div>
                    <p>{note.Text}</p>
                  </li>
                ))}
              </ul>
            ) : <Empty icon="scroll-text">{t('none')}</Empty>}
          </section>

          {detail.Related?.length ? (
            <section className="evidence-section">
              <h2>{l.Related}</h2>
              <ul className="evidence-list">
                {detail.Related.map(row => (
                  <li key={row.Href} className="evidence-row">
                    <Link href={row.Href}>{row.Title}</Link>
                    <Signal tone={row.SeverityClass}>{row.Severity}</Signal>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}

          {detail.Changes?.length ? (
            <section className="evidence-section">
              <h2>{l.Changes}</h2>
              <ul className="evidence-list">
                {detail.Changes.map((row, index) => (
                  <li key={index}>
                    {row.Href ? <a href={row.Href}><strong>{row.Summary}</strong></a> : <strong>{row.Summary}</strong>}
                    <p>{row.At} · {row.Source} · {row.About}</p>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}

          {(detail.Channel || detail.Agents?.length || detail.Runbook || detail.Dashboard) && (
            <section className="evidence-section">
              <h2>{l.Details}</h2>
              <div className="resource-tools">
                {detail.Runbook && <a className="button" href={detail.Runbook} target="_blank" rel="noopener"><Icon name="book-open" size="xs" /><span>{l.Runbook}</span></a>}
                {detail.Dashboard && <a className="button" href={detail.Dashboard} target="_blank" rel="noopener"><Icon name="external-link" size="xs" /><span>{l.Dashboard}</span></a>}
                {detail.Channel && <Signal tone={detail.Paused ? 'danger' : 'success'}>{detail.Channel}</Signal>}
              </div>
              {detail.Agents?.length ? (
                <div className="pill-row">
                  {detail.Agents.map(agent => <span key={agent} className="mono-chip">{agent}</span>)}
                </div>
              ) : null}
            </section>
          )}
        </div>
      </div>
    </AdminContext.Provider>
  )
}
