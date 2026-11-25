import { createRoot } from 'react-dom/client'
import { useEffect, useRef } from 'react'
import type { ReactNode } from 'react'
import type { Label, WorkspaceSession } from './api'
import { ViewPage } from './admin-pages'
import { Alerts } from './alerts'
import { Incident } from './incident'
import { base, Link, useLocation, workspaceHref } from './navigation'
import { Overview } from './overview'
import { useResource } from './resources'
import './styles.css'

function Frame({ session, href, t, children }: {
  session: WorkspaceSession; href: string; t: Label; children: ReactNode
}) {
  const menu = useRef<HTMLElement>(null)
  function closeMenu() {
    if (menu.current?.matches(':popover-open')) menu.current.hidePopover()
  }
  return <div className="app-frame">
    <a className="skip" href="#main">{t('skip')}</a>
    <aside ref={menu} id="navigation" className="sidebar" popover="auto" aria-label={t('menu')}>
      <div className="sidebar-heading">
      <Link href={base} className="brand" onClick={closeMenu}>
        <span className="brand-mark" aria-hidden="true"><span /></span>Beacon
      </Link>
      <button className="mobile-close" type="button" popoverTarget="navigation" popoverTargetAction="hide" aria-label={t('close')}>×</button>
      </div>
      <nav aria-label={t('menu')}>
        {session.navigation.map(group => <div className="nav-group" key={group.Label || 'start'}>
          {group.Label && <span>{group.Label}</span>}
          {group.Items.map(item => {
            const path = workspaceHref(item.Href)
            const active = path === base ? href === base : href === path || href.startsWith(path + '/')
            return <Link key={item.Key} href={workspaceHref(item.Href)} aria-current={active ? 'page' : undefined} onClick={closeMenu}>
              <span>{item.Label}</span>{item.Count && <span className="nav-count">{item.Count}</span>}
            </Link>
          })}
        </div>)}
      </nav>
      <div className="account">
        <Link href="/console/account" aria-label={t('account')}>{session.name}</Link>
        <form method="post" action="/console/sign-out">
          <input type="hidden" name="csrf" value={session.csrf} />
          <button>{t('signOut')}</button>
        </form>
        <a className="sidebar-docs" href={session.docs}>{t('docs')}</a>
      </div>
    </aside>
    <div className="stage">
      <div className="stage-bar">
        <button className="button nav-toggle" type="button" popoverTarget="navigation">
          {t('menu')}
        </button>
        <Link className="mobile-brand" href={base}>Beacon</Link>
      </div>
      <main id="main" tabIndex={-1}>{children}</main>
    </div>
  </div>
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
    return <ViewPage key={url.pathname + url.search} route={route} search={url.search} hash={url.hash}
      csrf="" t={key => session?.labels[key] ?? (key === 'failed' || key === 'loadError' ? 'Request failed' : '')} />
  }
  if (resource.error?.status === 401) return null
  if (resource.error) return <main className="entry" role="alert"><h1>Beacon</h1><p>The workspace could not connect to Beacon.</p><a href={signIn}>Sign in</a></main>
  if (!session) return <main className="entry" role="status">Beacon</main>
  const t: Label = key => session.labels[key] ?? key
  const incident = /^alerts\/[^/]+(?:\/episodes\/[^/]+)?$/.test(route)
  return <Frame session={session} href={url.pathname} t={t}>
    {route === '' ? <Overview t={t} /> : route === 'alerts' ? <Alerts query={url.search} t={t} /> : incident ?
      <Incident key={route} path={'/' + route + url.search} hash={url.hash} t={t} csrf={session.csrf} /> :
      <ViewPage key={url.pathname + url.search} route={route} search={url.search} hash={url.hash} csrf={session.csrf} t={t} />}
  </Frame>
}

createRoot(document.getElementById('root')!).render(<App />)
