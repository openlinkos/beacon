import type { Label, Metric, OverviewData } from './api'
import { Empty, Feed, Loading, PageHeading, Signal } from './components'
import { Link } from './navigation'
import { useResource } from './resources'

function MetricLink({ metric, showDetail = true }: { metric: Metric; showDetail?: boolean }) {
  const coverage = metric.Coverage
  return <Link className="metric" href={metric.Href}>
    <span>{metric.Label}</span>
    <strong data-tone={metric.Tone}>{metric.Value}</strong>
    {showDetail && metric.Meta && <small>{metric.Meta}</small>}
    {coverage && <meter min={0} max={coverage.Expected} value={coverage.Reporting} aria-hidden="true" />}
  </Link>
}

function NotificationRail({ data, t }: { data: OverviewData; t: Label }) {
  return <aside className="overview-rail">
    {data.notifications?.length ? <section className="rail-section" aria-labelledby="notifications">
      <h2 id="notifications">{t('notifications')}</h2>
      <ul className="notification-list">
        {data.notifications.map((row, index) =>
          <li className="notification" key={`${row.Channel}-${index}`}>
            <div className="notification-head">
              <strong>{row.Channel}</strong>
              <Signal tone={row.Tone}>{row.Result}</Signal>
            </div>
            {row.Schedule && <p>{row.Schedule}</p>}
            {row.DeviceAcknowledgement && <p>{row.DeviceAcknowledgement}</p>}
            {row.Href && <div className="notification-meta">
              <Link aria-label={row.OpenLabel} href={row.Href}>{t('openAlert')}</Link>
              <span>{row.At}</span>
            </div>}
          </li>)}
      </ul>
      {data.deliveryDrills?.length ? <div className="rail-links">
        {data.deliveryDrills.map(link => <a href={link.Href} key={link.Href}>{link.V}</a>)}
      </div> : null}
    </section> : null}
    <section className="rail-section" aria-labelledby="upcoming">
      <h2 id="upcoming">{t('upcoming')}</h2>
      <Feed rows={data.upcoming} empty={t('noUpcoming')} />
      <Link className="text-action" href={data.maintenance.Href}>{data.maintenance.V}</Link>
    </section>
    <section className="rail-section rail-metrics">
      <MetricLink metric={data.metrics.Machines} />
      <MetricLink metric={data.metrics.Sessions} showDetail={false} />
    </section>
  </aside>
}

export function Overview({ t }: { t: Label }) {
  const resource = useResource<OverviewData>('/overview')
  if (resource.error) return <Empty>{t('loadError')}</Empty>
  const data = resource.data
  if (!data) return <Loading t={t} />
  const attention = data.queue ?? []
  return <>
    <PageHeading title={t('overview')} actions={data.addMonitor.Href && <Link className="button" href={data.addMonitor.Href}>
      <span aria-hidden="true">+</span>{data.addMonitor.V}
    </Link>} />
    <section className="system-snapshot" data-tone={data.health.Tone} aria-labelledby="health">
      <div className="snapshot-state">
        <h2 id="health"><Signal tone={data.health.Tone}>{data.health.Title}</Signal></h2>
      </div>
      <div className="snapshot-metrics">
        {[data.metrics.Firing, data.metrics.Services, data.metrics.Locations].map(metric => <MetricLink metric={metric} key={metric.Href} />)}
      </div>
    </section>
    {data.deliveryHint && <div className="notice" role="status">
      <Signal tone="warning">{data.deliveryHint}</Signal><Link href={data.deliveryHelp.Href}>{data.deliveryHelp.V}</Link>
    </div>}
    <div className="overview-layout">
      <div className="overview-main">
        <section className="operations" aria-labelledby="attention">
          <header className="section-heading">
            <h2 id="attention">{t('attention')}</h2>
            <Link className="text-action" href="/console/alerts">{t('allAlerts')} <span aria-hidden="true">↗</span></Link>
          </header>
          {attention.length ? <ul className="queue">
            {attention.map((item, index) =>
              <li key={`${item.Href}-${index}`}>
                <div className="queue-item">
                  <Link href={item.Href}>{item.Title}</Link>
                  <span>{item.Sub}</span>
                </div>
                <div className="queue-state">
                  <Signal tone={item.BadgeTone}>{item.Badge}</Signal>
                  {item.Response && <span className={item.Unclaimed ? 'waiting' : ''}>{item.Response}</span>}
                  {item.DeviceResponse && <span>{item.DeviceResponse}</span>}
                  {item.Silenced && <span>{t('silenced')}</span>}
                </div>
                <span className="queue-age">{item.When}</span>
              </li>)}
          </ul> : <Empty>{t('calm')}</Empty>}
        </section>
        <section className="activity" aria-labelledby="activity">
          <header className="section-heading"><h2 id="activity">{t('activity')}</h2></header>
          <div className="activity-layout">
            <section><h3><Link href="/console/sessions">{t('sessions')}</Link></h3><Feed rows={data.sessions} empty={t('noSessions')} /></section>
            <section><h3><Link href="/console/changes">{t('changes')}</Link></h3><Feed rows={data.changes} empty={t('noChanges')} /></section>
          </div>
        </section>
      </div>
      <NotificationRail data={data} t={t} />
    </div>
  </>
}
