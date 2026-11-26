import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import type { FeedItem, Label, NavItem, Tone } from './api'
import { Icon } from './icons'
import { Link, navigate, workspaceHref } from './navigation'
import { setTheme } from './theme'

export function Signal({ children, tone = '', pulse = false }: { children: ReactNode; tone?: Tone; pulse?: boolean }) {
  return (
    <span className={`signal${pulse ? ' signal-pulse' : ''}`} data-tone={tone}>
      <span className="signal-dot" aria-hidden="true" />
      <span>{children}</span>
    </span>
  )
}

export function Empty({ children, icon = 'check-circle' }: { children: ReactNode; icon?: string }) {
  return (
    <div className="empty">
      <Icon name={icon} size="md" className="empty-icon" />
      <span>{children}</span>
    </div>
  )
}

export function Loading({ t }: { t: Label }) {
  return (
    <div className="skeleton-stage" role="status" aria-label={t('loading')}>
      <div className="skeleton-bar skeleton-title" />
      <div className="skeleton-grid">
        <div className="skeleton-card" />
        <div className="skeleton-card" />
        <div className="skeleton-card" />
      </div>
      <div className="skeleton-panel" />
    </div>
  )
}

export function PageHeading({ title, subtitle, badge, actions }: {
  title: string; subtitle?: string; badge?: ReactNode; actions?: ReactNode
}) {
  return (
    <header className="page-heading">
      <div className="page-heading-copy">
        <div className="page-heading-title">
          <h1>{title}</h1>
          {badge}
        </div>
        {subtitle && <p className="page-subtitle">{subtitle}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  )
}

export function CopyBlock({ code, title, label, copyLabel = 'Copy', copiedLabel = 'Copied' }: {
  code: string; title?: string; label?: string; copyLabel?: string; copiedLabel?: string
}) {
  const [state, setState] = useState<'idle' | 'copied'>('idle')
  function handleCopy() {
    navigator.clipboard.writeText(code).then(() => {
      setState('copied')
      window.setTimeout(() => setState('idle'), 1800)
    }, () => undefined)
  }
  return (
    <div className="code-card">
      <div className="code-card-head">
        <div className="code-card-meta">
          {title && <strong>{title}</strong>}
          {label && <span className="code-card-tag">{label}</span>}
        </div>
        <button className="copy-btn" type="button" onClick={handleCopy}>
          <Icon name={state === 'copied' ? 'check' : 'copy'} size="xs" />
          <span>{state === 'copied' ? copiedLabel : copyLabel}</span>
        </button>
      </div>
      <pre className="code-card-body" tabIndex={0}><code>{code}</code></pre>
    </div>
  )
}

export function Feed({ rows, empty, icon = 'history' }: { rows: FeedItem[] | null; empty: string; icon?: string }) {
  if (!rows?.length) return <Empty icon={icon}>{empty}</Empty>
  return (
    <ul className="feed">
      {rows.map((item, index) => (
        <li key={`${item.Href || item.Title}-${index}`}>
          <div className="feed-main">
            {item.Href ? <Link href={item.Href}>{item.Title}</Link> : <strong>{item.Title}</strong>}
            {item.Sub && <span>{item.Sub}</span>}
          </div>
          <div className="feed-aside">
            {item.Badge && <Signal tone={item.Tone}>{item.Badge}</Signal>}
            <span>{item.When}</span>
          </div>
        </li>
      ))}
    </ul>
  )
}

type JumpEntry = {
  id: string
  label: string
  group: string
  icon: string
  count?: string
  action: () => void
}

export function CommandPalette({ open, onClose, navigation, homeHref, docsHref, openApiHref, t }: {
  open: boolean
  onClose: () => void
  navigation: { Label: string; Items: NavItem[] }[]
  homeHref: string
  docsHref: string
  openApiHref: string
  t: Label
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(0)

  useEffect(() => {
    const el = dialog.current
    if (!el) return
    if (open) {
      setQuery('')
      setSelected(0)
      if (!el.open) el.showModal()
      window.setTimeout(() => input.current?.focus(), 20)
    } else if (el.open) {
      el.close()
    }
  }, [open])

  const entries: JumpEntry[] = []
  for (const group of navigation) {
    for (const item of group.Items) {
      entries.push({
        id: item.Key,
        label: item.Label,
        group: group.Label || t('overview'),
        icon: item.Icon || 'chevron-right',
        count: item.Count,
        action: () => navigate(workspaceHref(item.Href)),
      })
    }
  }
  entries.push(
    { id: 'theme-light', label: `${t('theme')}: ${t('themeLight')}`, group: t('theme'), icon: 'sun', action: () => setTheme('light') },
    { id: 'theme-dark', label: `${t('theme')}: ${t('themeDark')}`, group: t('theme'), icon: 'moon', action: () => setTheme('dark') },
    { id: 'theme-system', label: `${t('theme')}: ${t('themeSystem')}`, group: t('theme'), icon: 'monitor', action: () => setTheme('system') },
    { id: 'openapi', label: t('openApiSpec'), group: t('mcpHub'), icon: 'external-link', action: () => window.open(openApiHref, '_blank', 'noopener') },
    { id: 'home', label: t('publicHome'), group: 'Beacon', icon: 'home', action: () => location.assign(homeHref) },
    { id: 'docs', label: t('docs'), group: 'Beacon', icon: 'book-open', action: () => window.open(docsHref, '_blank', 'noopener') },
  )

  const q = query.trim().toLowerCase()
  const filtered = q
    ? entries.filter(e => e.label.toLowerCase().includes(q) || e.group.toLowerCase().includes(q) || e.id.toLowerCase().includes(q))
    : entries

  const activeIndex = Math.min(selected, Math.max(0, filtered.length - 1))

  function run(entry: JumpEntry) {
    onClose()
    entry.action()
  }

  return (
    <dialog
      ref={dialog}
      className="palette"
      aria-label={t('quickJump')}
      onClose={onClose}
      onClick={e => { if (e.target === dialog.current) onClose() }}
    >
      <div className="palette-head">
        <Icon name="search" size="sm" />
        <input
          ref={input}
          type="text"
          value={query}
          onChange={e => { setQuery(e.target.value); setSelected(0) }}
          onKeyDown={e => {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              setSelected(i => Math.min(filtered.length - 1, i + 1))
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              setSelected(i => Math.max(0, i - 1))
            } else if (e.key === 'Enter' && filtered[activeIndex]) {
              e.preventDefault()
              run(filtered[activeIndex])
            }
          }}
          placeholder={t('jumpPlaceholder')}
          aria-label={t('quickJump')}
        />
        <kbd>ESC</kbd>
      </div>
      <div className="palette-list" role="listbox">
        {filtered.length ? filtered.map((entry, idx) => (
          <button
            key={entry.id}
            type="button"
            role="option"
            aria-selected={idx === activeIndex}
            className={`palette-item${idx === activeIndex ? ' is-active' : ''}`}
            onMouseEnter={() => setSelected(idx)}
            onClick={() => run(entry)}
          >
            <Icon name={entry.icon} size="sm" />
            <span className="palette-label">{entry.label}</span>
            <span className="palette-group">{entry.group}</span>
            {entry.count && <span className="nav-count">{entry.count}</span>}
          </button>
        )) : <p className="palette-empty">{t('noJumpResults')}</p>}
      </div>
    </dialog>
  )
}
