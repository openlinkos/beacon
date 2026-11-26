import { useRef } from 'react'
import type { AlertData, Label } from './api'
import { Empty, Loading, PageHeading, Signal } from './components'
import { Icon } from './icons'
import { Link, navigate } from './navigation'
import { useResource } from './resources'

export function Alerts({ query, t }: { query: string; t: Label }) {
  const response = useResource<AlertData>('/alerts' + query)
  const dialog = useRef<HTMLDialogElement>(null)
  if (response.error) return <Empty icon="triangle-alert">{t('loadError')}</Empty>
  if (!response.data) return <Loading t={t} />
  const { alerts, views, list } = response.data
  return (
    <>
      <PageHeading
        title={t('alerts')}
        actions={
          <Link href="/console/incident-outcomes" className="button">
            <Icon name="clock" size="xs" />
            <span>{t('history')}</span>
          </Link>
        }
      />

      <nav className="view-switches" aria-label={t('alerts')}>
        {views.map(view => (
          <Link href={view.Href} key={view.Href} aria-current={view.On ? 'page' : undefined}>
            <span>{view.Label}</span>
            {view.Count && <span className="tab-count">{view.Count}</span>}
          </Link>
        ))}
      </nav>

      <section className="alert-register">
        <div className="alert-toolbar">
          <form
            role="search"
            className="search-input-wrap"
            onSubmit={event => {
              event.preventDefault()
              const q = new FormData(event.currentTarget).get('q')
              const params = new URLSearchParams(query)
              params.delete('page')
              if (typeof q === 'string' && q.trim()) params.set('q', q.trim())
              else params.delete('q')
              navigate('/workspace/alerts?' + params)
            }}
          >
            <Icon name="search" size="xs" />
            <input
              key={list.Query}
              name="q"
              type="search"
              defaultValue={list.Query}
              placeholder={list.SearchHint}
              aria-label={t('search')}
            />
          </form>
          <button className="button" type="button" onClick={() => dialog.current?.showModal()}>
            <Icon name="sliders" size="xs" />
            <span>{t('filters')}</span>
            {list.On > 0 && <span className="tally">{list.On}</span>}
          </button>
          {list.Tags?.length ? (
            <div className="filter-tags">
              {list.Tags.map(tag => (
                <Link key={tag.Href} href={tag.Href}>
                  <span>{tag.Label}</span>
                  <Icon name="x" size="xs" />
                </Link>
              ))}
            </div>
          ) : null}
        </div>

        {alerts?.length ? (
          <div className="table-scroll">
            <table className="alerts-table">
              <thead>
                <tr>
                  <th scope="col">{t('title')}</th>
                  <th scope="col">{t('severity')}</th>
                  <th scope="col">{t('state')}</th>
                  <th scope="col">{t('service')}</th>
                  <th scope="col">{t('since')}</th>
                </tr>
              </thead>
              <tbody>
                {alerts.map(row => (
                  <tr key={row.Href}>
                    <td>
                      <Link href={row.Href}>{row.Title}</Link>
                      {(row.DeviceResponse || row.Acked) && <small>{row.DeviceResponse || t('acknowledged')}</small>}
                      {row.Silenced && <small>{t('silenced')}</small>}
                    </td>
                    <td data-label={t('severity')}>
                      <Signal tone={row.SeverityTone} pulse={row.SeverityTone === 'danger' && row.StateTone === 'danger'}>
                        {row.Severity}
                      </Signal>
                    </td>
                    <td data-label={t('state')}>
                      <Signal tone={row.StateTone}>{row.State}</Signal>
                    </td>
                    <td data-label={t('service')}><span className="mono-chip">{row.Service}</span></td>
                    <td data-label={t('since')}>{row.Since}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : <Empty icon="check-circle">{list.Empty}</Empty>}

        {list.Pager.Summary && (
          <nav className="pager" aria-label={t('alerts')}>
            <span>{list.Pager.Summary}</span>
            <div className="pager-links">
              {list.Pager.Prev && <Link href={list.Pager.Prev}>{t('prev')}</Link>}
              {list.Pager.Next && <Link href={list.Pager.Next}>{t('next')}</Link>}
            </div>
          </nav>
        )}
      </section>

      <dialog ref={dialog} className="filters" aria-labelledby="filter-title">
        <header>
          <h2 id="filter-title">{t('filters')}</h2>
          <form method="dialog">
            <button className="icon-btn" aria-label={t('close')}><Icon name="x" size="sm" /></button>
          </form>
        </header>
        {list.Facets?.map(facet => (
          <section key={facet.Title}>
            <h3>{facet.Title}</h3>
            <ul>
              {facet.Values?.map(value => (
                <li key={value.Href}>
                  <Link href={value.Href} aria-current={value.On ? 'true' : undefined} onClick={() => dialog.current?.close()}>
                    <span>{value.Label}</span>
                    <span className="tab-count">{value.Count}</span>
                  </Link>
                </li>
              ))}
            </ul>
            {facet.More && <p className="facet-more">{facet.More}</p>}
          </section>
        ))}
      </dialog>
    </>
  )
}
