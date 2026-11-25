import { useRef } from 'react'
import type { AlertData, Label } from './api'
import { Empty, Loading, PageHeading, Signal } from './components'
import { Link, navigate } from './navigation'
import { useResource } from './resources'

export function Alerts({ query, t }: { query: string; t: Label }) {
  const response = useResource<AlertData>('/alerts' + query)
  const dialog = useRef<HTMLDialogElement>(null)
  if (response.error) return <Empty>{t('loadError')}</Empty>
  if (!response.data) return <Loading t={t} />
  const { alerts, views, list } = response.data
  return <>
    <PageHeading title={t('alerts')} actions={<a href="/console/incident-outcomes" className="button">{t('history')}</a>} />
    <nav className="view-switches" aria-label={t('alerts')}>
      {views.map(view => <Link href={view.Href} key={view.Href} aria-current={view.On ? 'page' : undefined}>{view.Label}</Link>)}
    </nav>
    <section className="alert-register">
      <div className="alert-toolbar">
        <form role="search" onSubmit={event => {
          event.preventDefault()
          const q = new FormData(event.currentTarget).get('q')
          const params = new URLSearchParams(query)
          params.delete('page')
          if (typeof q === 'string' && q.trim()) params.set('q', q.trim())
          else params.delete('q')
          navigate('/workspace/alerts?' + params)
        }}>
          <input key={list.Query} name="q" type="search" defaultValue={list.Query} placeholder={list.SearchHint} aria-label={t('search')} />
        </form>
        <button className="button" type="button" onClick={() => dialog.current?.showModal()}>{t('filters')}{list.On > 0 && <span className="tally">{list.On}</span>}</button>
        {list.Tags?.length ? <div className="filter-tags">
          {list.Tags.map(tag => <Link key={tag.Href} href={tag.Href}>{tag.Label} <span aria-hidden="true">×</span></Link>)}
        </div> : null}
      </div>
      {alerts?.length ? <table className="alerts-table">
        <thead><tr>
          <th scope="col">{t('title')}</th><th scope="col">{t('severity')}</th><th scope="col">{t('state')}</th>
          <th scope="col">{t('service')}</th><th scope="col">{t('since')}</th>
        </tr></thead>
        <tbody>{alerts.map(row => <tr key={row.Href}>
          <td><Link href={row.Href}>{row.Title}</Link><small>{row.DeviceResponse || (row.Acked ? t('acknowledged') : '')}</small>{row.Silenced && <small>{t('silenced')}</small>}</td>
          <td data-label={t('severity')}><Signal tone={row.SeverityTone}>{row.Severity}</Signal></td>
          <td data-label={t('state')}><Signal tone={row.StateTone}>{row.State}</Signal></td>
          <td data-label={t('service')}>{row.Service}</td><td data-label={t('since')}>{row.Since}</td>
        </tr>)}</tbody>
      </table> : <Empty>{list.Empty}</Empty>}
      {list.Pager.Summary && <nav className="pager" aria-label={t('alerts')}>
        <span>{list.Pager.Summary}</span>
        {list.Pager.Prev && <Link href={list.Pager.Prev}>{t('prev')}</Link>}
        {list.Pager.Next && <Link href={list.Pager.Next}>{t('next')}</Link>}
      </nav>}
    </section>
    <dialog ref={dialog} className="filters" aria-labelledby="filter-title">
      <header><h2 id="filter-title">{t('filters')}</h2><form method="dialog"><button className="button">{t('close')}</button></form></header>
      {list.Facets?.map(facet => <section key={facet.Title}>
        <h3>{facet.Title}</h3>
        <ul>{facet.Values?.map(value => <li key={value.Href}>
          <Link href={value.Href} aria-current={value.On ? 'true' : undefined} onClick={() => dialog.current?.close()}>
            <span>{value.Label}</span><span>{value.Count}</span>
          </Link>
        </li>)}</ul>
        {facet.More && <p>{facet.More}</p>}
      </section>)}
    </dialog>
  </>
}
