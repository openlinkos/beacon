import type { ReactNode } from 'react'
import type { FeedItem, Label, Tone } from './api'
import { Link } from './navigation'

export function Signal({ children, tone = '' }: { children: ReactNode; tone?: Tone }) {
  return <span className="signal" data-tone={tone}><span className="signal-dot" aria-hidden="true" /><span>{children}</span></span>
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="empty">{children}</p>
}

export function Loading({ t }: { t: Label }) {
  return <p className="empty" role="status">{t('loading')}</p>
}

export function PageHeading({ title, actions }: { title: string; actions?: ReactNode }) {
  return <header className="page-heading"><h1>{title}</h1>{actions && <div className="page-actions">{actions}</div>}</header>
}

export function Feed({ rows, empty }: { rows: FeedItem[] | null; empty: string }) {
  if (!rows?.length) return <Empty>{empty}</Empty>
  return <ul className="feed">{rows.map((item, index) =>
    <li key={`${item.Href || item.Title}-${index}`}>
      <div className="feed-main">{item.Href ? <Link href={item.Href}>{item.Title}</Link> : <strong>{item.Title}</strong>}
        <span>{item.Sub}</span>
      </div>
      <div className="feed-aside">{item.Badge && <Signal tone={item.Tone}>{item.Badge}</Signal>}<span>{item.When}</span></div>
    </li>
  )}</ul>
}
