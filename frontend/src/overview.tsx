import type { Label, Metric, OverviewData } from './api'
import { Empty, Feed, Loading, PageHeading, Signal } from './components'
import { Icon } from './icons'
import { Link } from './navigation'
import { useResource } from './resources'

function MetricLink({ metric, icon, showDetail = true }: { metric: Metric; icon?: string; showDetail?: boolean }) {
  const coverage = metric.Coverage
  return (
    <Link className="metric" href={metric.Href} data-tone={metric.Tone}>
      <div className="metric-head">
        <span>{metric.Label}</span>
        {icon && <Icon name={icon} size="xs" className="metric-icon" />}
      </div>
      <strong data-tone={metric.Tone}>{metric.Value}</strong>
      {showDetail && metric.Meta && <small>{metric.Meta}</small>}
      {coverage && <meter min={0} max={coverage.Expected} value={coverage.Reporting} aria-hidden="true" />}
    </Link>
  )
}

function NotificationRail({ data, t }: { data: OverviewData; t: Label }) {
  return (
    <aside className="overview-rail">
      {data.notifications?.length ? (
        <section className="rail-section" aria-labelledby="notifications">
          <header className="rail-head">
            <Icon name="zap" size="xs" />
            <h2 id="notifications">{t('notifications')}</h2>
          </header>
          <ul className="notification-list">
            {data.notifications.map((row, index) => (
              <li className="notification" key={`${row.Channel}-${index}`}>
                <div className="notification-head">
                  <strong>{row.Channel}</strong>
                  <Signal tone={row.Tone}>{row.Result}</Signal>
                </div>
                {row.Schedule && <p>{row.Schedule}</p>}
                {row.DeviceAcknowledgement && <p>{row.DeviceAcknowledgement}</p>}
                {row.Href && (
                  <div className="notification-meta">
                    <Link aria-label={row.OpenLabel} href={row.Href}>{t('openAlert')}</Link>
                    <span>{row.At}</span>
                  </div>
                )}
              </li>
            ))}
          </ul>
          {data.deliveryDrills?.length ? (
            <div className="rail-links">
              {data.deliveryDrills.map(link => <a href={link.Href} key={link.Href}>{link.V}</a>)}
            </div>
          ) : null}
        </section>
      ) : null}

      <section className="rail-section" aria-labelledby="upcoming">
        <header className="rail-head">
          <Icon name="timer" size="xs" />
          <h2 id="upcoming">{t('upcoming')}</h2>
        </header>
        <Feed rows={data.upcoming} empty={t('noUpcoming')} icon="clock" />
        <Link className="text-action" href={data.maintenance.Href}>
          <span>{data.maintenance.V}</span>
          <Icon name="arrow-up-right" size="xs" />
        </Link>
      </section>

      <section className="rail-section rail-metrics">
        <MetricLink metric={data.metrics.Machines} icon="server" />
        <MetricLink metric={data.metrics.Sessions} icon="terminal" showDetail={false} />
      </section>
    </aside>
  )
}

export function Overview({ t }: { t: Label }) {
  const resource = useResource<OverviewData>('/overview')
  if (resource.error) return <Empty icon="triangle-alert">{t('loadError')}</Empty>
  const data = resource.data
  if (!data) return <Loading t={t} />
  const attention = data.queue ?? []

  return (
    <>
      <PageHeading
        title={t('overview')}
        actions={
          data.addMonitor.Href && (
            <Link className="button primary" href={data.addMonitor.Href}>
              <Icon name="plus" size="xs" />
              <span>{data.addMonitor.V}</span>
            </Link>
          )
        }
      />

      <section className="system-snapshot" data-tone={data.health.Tone} aria-labelledby="health">
        <div className="snapshot-state">
          <h2 id="health">
            <Signal tone={data.health.Tone} pulse={data.health.Tone === 'danger' || data.health.Tone === 'success'}>
              {data.health.Title}
            </Signal>
          </h2>
          {data.health.Text && <p className="snapshot-text">{data.health.Text}</p>}
        </div>
        <div className="snapshot-metrics">
          <MetricLink metric={data.metrics.Firing} icon="triangle-alert" />
          <MetricLink metric={data.metrics.Services} icon="activity" />
          <MetricLink metric={data.metrics.Locations} icon="globe" />
        </div>
      </section>

      {data.deliveryHint && (
        <div className="notice" role="status">
          <Signal tone="warning">{data.deliveryHint}</Signal>
          <Link href={data.deliveryHelp.Href}>{data.deliveryHelp.V}</Link>
        </div>
      )}

      <div className="overview-layout">
        <div className="overview-main">
          <section className="operations" aria-labelledby="attention">
            <header className="section-heading">
              <div className="section-title-icon">
                <Icon name="triangle-alert" size="sm" />
                <h2 id="attention">{t('attention')}</h2>
              </div>
              <Link className="text-action" href="/console/alerts">
                <span>{t('allAlerts')}</span>
                <Icon name="arrow-up-right" size="xs" />
              </Link>
            </header>
            {attention.length ? (
              <ul className="queue">
                {attention.map((item, index) => (
                  <li key={`${item.Href}-${index}`} data-tone={item.BadgeTone}>
                    <div className="queue-item">
                      <Link href={item.Href}>{item.Title}</Link>
                      <span>{item.Sub}</span>
                    </div>
                    <div className="queue-state">
                      <Signal tone={item.BadgeTone} pulse={item.BadgeTone === 'danger'}>{item.Badge}</Signal>
                      {item.Response && <span className={item.Unclaimed ? 'waiting' : 'owner-chip'}>{item.Response}</span>}
                      {item.DeviceResponse && <span className="device-chip">{item.DeviceResponse}</span>}
                      {item.Silenced && <span className="muted-chip">{t('silenced')}</span>}
                    </div>
                    <span className="queue-age">{item.When}</span>
                  </li>
                ))}
              </ul>
            ) : <Empty icon="check-circle">{t('calm')}</Empty>}
          </section>

          <section className="activity" aria-labelledby="activity">
            <header className="section-heading">
              <div className="section-title-icon">
                <Icon name="activity" size="sm" />
                <h2 id="activity">{t('activity')}</h2>
              </div>
            </header>
            <div className="activity-layout">
              <section className="activity-card">
                <h3>
                  <Link href="/console/sessions">
                    <Icon name="terminal" size="xs" />
                    <span>{t('sessions')}</span>
                  </Link>
                </h3>
                <Feed rows={data.sessions} empty={t('noSessions')} icon="terminal" />
              </section>
              <section className="activity-card">
                <h3>
                  <Link href="/console/changes">
                    <Icon name="history" size="xs" />
                    <span>{t('changes')}</span>
                  </Link>
                </h3>
                <Feed rows={data.changes} empty={t('noChanges')} icon="history" />
              </section>
            </div>
          </section>
        </div>

        <NotificationRail data={data} t={t} />
      </div>
    </>
  )
}
