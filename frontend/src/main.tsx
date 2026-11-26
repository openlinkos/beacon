import { createRoot } from 'react-dom/client'
import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import type { Label, WorkspaceSession } from './api'
import { ViewPage } from './admin-pages'
import { Alerts } from './alerts'
import { CapabilitiesHub } from './capabilities-page'
import { CommandPalette, Signal } from './components'
import { BeaconMark, Icon } from './icons'
import { Incident } from './incident'
import { base, Link, useLocation, workspaceHref } from './navigation'
import { Overview } from './overview'
import { useResource } from './resources'
import { useTheme } from './theme'
import type { ThemeMode } from './theme'
import './styles.css'

const publicFallbackLabels: Record<string, string> = {
  failed: 'Request failed',
  loadError: 'The workspace could not load this page.',
  loading: 'Loading workspace',
  backHome: 'Back to home',
  signInHeadline: 'Human & AI operations infrastructure',
  signInTagline: 'Multi-location consensus probing, short-lived SSH certificates with replay, and a native MCP server you control.',
  highlightProbe: 'Multi-location consensus',
  highlightSSH: '5-min SSH certificates',
  highlightMCP: 'Native MCP + OAuth 2.1',
}

function ThemeToggle({ t }: { t: Label }) {
  const [mode, setMode] = useTheme()
  const options: { value: ThemeMode; icon: string; label: string }[] = [
    { value: 'light', icon: 'sun', label: t('themeLight') },
    { value: 'dark', icon: 'moon', label: t('themeDark') },
    { value: 'system', icon: 'monitor', label: t('themeSystem') },
  ]
  return (
    <div className="theme-switch" role="group" aria-label={t('theme')}>
      {options.map(opt => (
        <button
          key={opt.value}
          type="button"
          className={`theme-btn${mode === opt.value ? ' is-active' : ''}`}
          aria-pressed={mode === opt.value}
          title={opt.label}
          aria-label={opt.label}
          onClick={() => setMode(opt.value)}
        >
          <Icon name={opt.icon} size="xs" />
        </button>
      ))}
    </div>
  )
}

function Frame({ session, href, t, children }: {
  session: WorkspaceSession; href: string; t: Label; children: ReactNode
}) {
  const menu = useRef<HTMLElement>(null)
  const [paletteOpen, setPaletteOpen] = useState(false)

  function closeMenu() {
    if (menu.current?.matches(':popover-open')) menu.current.hidePopover()
  }

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPaletteOpen(open => !open)
      }
    }
    addEventListener('keydown', onKeyDown)
    return () => removeEventListener('keydown', onKeyDown)
  }, [])

  let activeLabel = t('overview')
  for (const group of session.navigation) {
    for (const item of group.Items) {
      const path = workspaceHref(item.Href)
      const isMatch = path === base ? href === base : href === path || href.startsWith(path + '/')
      if (isMatch) activeLabel = item.Label
    }
  }
  const initial = (session.name || 'B').trim().slice(0, 1).toUpperCase()

  return (
    <div className="app-frame">
      <a className="skip" href="#main">{t('skip')}</a>
      <aside ref={menu} id="navigation" className="sidebar" popover="auto" aria-label={t('menu')}>
        <div className="sidebar-heading">
          <Link href={base} className="brand" onClick={closeMenu}>
            <BeaconMark />
            <span className="brand-name">Beacon</span>
          </Link>
          <button className="mobile-close" type="button" popoverTarget="navigation" popoverTargetAction="hide" aria-label={t('close')}>
            <Icon name="x" size="sm" />
          </button>
        </div>

        <nav className="sidebar-nav" aria-label={t('menu')}>
          {session.navigation.map(group => (
            <div className="nav-group" key={group.Label || 'start'}>
              {group.Label && <span className="nav-group-label">{group.Label}</span>}
              {group.Items.map(item => {
                const path = workspaceHref(item.Href)
                const active = path === base ? href === base : href === path || href.startsWith(path + '/')
                return (
                  <Link
                    key={item.Key}
                    href={path}
                    className="nav-item"
                    aria-current={active ? 'page' : undefined}
                    onClick={closeMenu}
                  >
                    <Icon name={item.Icon || 'chevron-right'} size="sm" />
                    <span className="nav-item-label">{item.Label}</span>
                    {item.Count && <span className="nav-count" data-tone={item.Key === 'alerts' ? 'danger' : ''}>{item.Count}</span>}
                  </Link>
                )
              })}
            </div>
          ))}
        </nav>

        <div className="sidebar-footer">
          {session.sshChannel?.Text && (
            <Link className="channel-pill" href={session.sshChannel.Href} onClick={closeMenu}>
              <Signal tone={session.sshChannel.Tone} pulse={session.sshChannel.Tone === 'success'}>
                {session.sshChannel.Text}
              </Signal>
            </Link>
          )}
          <div className="account-card">
            <Link className="account-identity" href="/console/account" aria-label={t('account')} onClick={closeMenu}>
              <span className="account-avatar" aria-hidden="true">{initial}</span>
              <span className="account-copy">
                <strong>{session.name}</strong>
                {session.whoScopes && <small>{session.whoScopes}</small>}
              </span>
            </Link>
            <form method="post" action="/console/sign-out">
              <input type="hidden" name="csrf" value={session.csrf} />
              <button className="icon-btn" type="submit" title={t('signOut')} aria-label={t('signOut')}>
                <Icon name="log-out" size="sm" />
              </button>
            </form>
          </div>
          <div className="sidebar-meta">
            <ThemeToggle t={t} />
            <div className="sidebar-links">
              <a href={session.homeHref} title={t('publicHome')} aria-label={t('publicHome')}><Icon name="home" size="xs" /></a>
              <a href={session.docs} target="_blank" rel="noopener" title={t('docs')} aria-label={t('docs')}><Icon name="book-open" size="xs" /></a>
            </div>
          </div>
        </div>
      </aside>

      <div className="stage">
        <header className="topbar">
          <div className="topbar-left">
            <button className="icon-btn nav-toggle" type="button" popoverTarget="navigation" aria-label={t('menu')}>
              <Icon name="menu" size="sm" />
            </button>
            <Link className="mobile-brand" href={base}><BeaconMark /><span>Beacon</span></Link>
            <span className="topbar-crumb">{activeLabel}</span>
          </div>
          <div className="topbar-right">
            <button className="jump-trigger" type="button" onClick={() => setPaletteOpen(true)}>
              <Icon name="search" size="xs" />
              <span>{t('quickJump')}</span>
              <kbd>⌘K</kbd>
            </button>
            <Link className="topbar-chip" href="/workspace/capabilities">
              <Icon name="layers" size="xs" />
              <span>{t('mcpHub')}</span>
            </Link>
          </div>
        </header>
        <main id="main" tabIndex={-1}>{children}</main>
        <footer className="workspace-footer">
          <span>{session.version || 'Beacon'}</span>
          <div className="workspace-footer-links">
            <a href={session.homeHref}>{t('publicHome')}</a>
            <a href={session.docs} target="_blank" rel="noopener">{t('docs')}</a>
            {session.repo && <a href={session.repo} target="_blank" rel="noopener">GitHub</a>}
          </div>
        </footer>
      </div>

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        navigation={session.navigation}
        homeHref={session.homeHref}
        docsHref={session.docs}
        openApiHref={session.openApiHref}
        t={t}
      />
    </div>
  )
}

function App() {
  const url = new URL(useLocation())
  const resource = useResource<WorkspaceSession>('/session')
  const session = resource.data
  const route = url.pathname.slice(base.length).replace(/\/$/, '')
  const publicEntry = ['sign-in', 'setup', 'join', 'reset'].includes(route)
  const signIn = '/workspace/sign-in?next=' + encodeURIComponent(url.pathname + url.search + url.hash)

  useEffect(() => {
    if (session) document.documentElement.lang = session.language
  }, [session])

  useEffect(() => {
    if (!publicEntry && resource.error?.status === 401) location.replace(signIn)
  }, [publicEntry, resource.error, signIn])

  if (publicEntry) {
    return (
      <ViewPage
        key={url.pathname + url.search}
        route={route}
        search={url.search}
        hash={url.hash}
        csrf=""
        t={key => session?.labels[key] ?? publicFallbackLabels[key] ?? ''}
      />
    )
  }
  if (resource.error?.status === 401) return null
  if (resource.error) {
    return (
      <main className="auth-shell">
        <div className="auth-card" role="alert">
          <div className="auth-brand-inline"><BeaconMark /><span>Beacon</span></div>
          <h1>Connection interrupted</h1>
          <p>The workspace could not connect to Beacon.</p>
          <a className="button primary" href={signIn}>Sign in</a>
        </div>
      </main>
    )
  }
  if (!session) {
    return (
      <main className="boot-screen" role="status" aria-label="Loading Beacon">
        <BeaconMark className="boot-mark" />
        <span>Beacon</span>
      </main>
    )
  }

  const t: Label = key => session.labels[key] ?? key
  const incident = /^alerts\/[^/]+(?:\/episodes\/[^/]+)?$/.test(route)
  return (
    <Frame session={session} href={url.pathname} t={t}>
      {route === '' ? <Overview t={t} />
        : route === 'capabilities' ? <CapabilitiesHub session={session} t={t} />
        : route === 'alerts' ? <Alerts query={url.search} t={t} />
        : incident ? <Incident key={route} path={'/' + route + url.search} hash={url.hash} t={t} csrf={session.csrf} />
        : <ViewPage key={url.pathname + url.search} route={route} search={url.search} hash={url.hash} csrf={session.csrf} t={t} />}
    </Frame>
  )
}

createRoot(document.getElementById('root')!).render(<App />)
