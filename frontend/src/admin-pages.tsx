import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { AdminContext, useView, visit } from './admin'
import type { ViewResult } from './admin'
import { Account, AccountLinkForm, Agent, Agents, Once, People, SignInForm } from './access-pages'
import type { AccountData, AgentData, AgentsData, OnceData, OneTimeForm, PeopleData, SignInData } from './access-pages'
import { Empty, Loading, PageHeading } from './components'
import type { Label } from './api'
import { Machine, MachineHistory, Machines } from './machine-pages'
import type { MachineData, MachineList } from './machine-pages'
import { Monitor, MonitorForm, Monitors, ResponseMachine } from './monitor-pages'
import type { MonitorData, MonitorFormData, MonitorsData } from './monitor-pages'
import { Session, Sessions } from './session-pages'
import type { SessionData, SessionsData } from './session-pages'
import { History, Log, Outcome } from './list-pages'
import { Maintenance, Publication, Silences, StatusDetail, StatusPages } from './publication-pages'
import { Config, Credentials, ShareLinks, Tokens, Version } from './settings-pages'
import { Link } from './navigation'

function pageBody(route: string, data: unknown): ReactNode {
  if (route.endsWith('/share-links')) return <ShareLinks data={data as Parameters<typeof ShareLinks>[0]['data']} />
  if (route === 'account') return <Account data={data as AccountData} />
  if (route === 'people') return <People data={data as PeopleData} />
  if (route === 'agents') return <Agents data={data as AgentsData} />
  if (route.startsWith('agents/')) return <Agent data={data as AgentData} />
  if (route === 'machines') return <Machines data={data as MachineList} />
  if (route.startsWith('machines/') && route.endsWith('/history')) return <MachineHistory data={data as Parameters<typeof MachineHistory>[0]['data']} />
  if (route.startsWith('machines/')) return <Machine data={data as MachineData} />
  if (route === 'monitors') return <Monitors data={data as MonitorsData} />
  if (route === 'monitors/add' || (route.startsWith('monitors/') && route.endsWith('/edit'))) return <MonitorForm data={data as MonitorFormData} />
  if (route.includes('/response-machine/')) return <ResponseMachine data={data as Parameters<typeof ResponseMachine>[0]['data']} />
  if (route.startsWith('monitors/')) return <Monitor data={data as MonitorData} />
  if (route === 'sessions') return <Sessions data={data as SessionsData} />
  if (route.startsWith('sessions/')) return <Session data={data as SessionData} />
  if (route === 'silences') return <Silences data={data as Parameters<typeof Silences>[0]['data']} />
  if (route === 'maintenance/new') return <Maintenance data={data as Parameters<typeof Maintenance>[0]['data']} />
  if (route === 'status-pages') return <StatusPages data={data as Parameters<typeof StatusPages>[0]['data']} />
  if (route === 'status-pages/new' || route.includes('/components/')) return <Publication data={data as Parameters<typeof Publication>[0]['data']} />
  if (route.startsWith('status-pages/')) return <StatusDetail data={data as Parameters<typeof StatusDetail>[0]['data']} />
  if (route === 'config') return <Config data={data as Parameters<typeof Config>[0]['data']} />
  if (route.startsWith('config/history/')) return <Version data={data as Parameters<typeof Version>[0]['data']} />
  if (route === 'tokens') return <Tokens data={data as Parameters<typeof Tokens>[0]['data']} />
  if (route === 'credentials' || route.startsWith('credentials/')) return <Credentials data={data as Parameters<typeof Credentials>[0]['data']} />
  if (['audit', 'changes', 'hooks'].includes(route)) return <Log route={route} data={data as Parameters<typeof Log>[0]['data']} />
  if (route === 'incident-outcomes' || route === 'incident-episodes') return <History episodes={route === 'incident-episodes'} data={data as Parameters<typeof History>[0]['data']} />
  if (route.startsWith('incident-outcomes/')) return <Outcome data={data as Parameters<typeof Outcome>[0]['data']} />
  return null
}

export function ViewPage({ route, search, hash, csrf, t }: { route: string; search: string; hash: string; csrf: string; t: Label }) {
  const [revision, setRevision] = useState(0)
  const [submitted, setSubmitted] = useState<ViewResult<unknown>>()
  const trailingSlash = route === 'monitors/add' || route === 'status-pages/new'
  const resource = useView<unknown>('/console/' + route + (trailingSlash ? '/' : '') + search, revision)
  const page = submitted ?? resource?.page
  const signIn = route === 'sign-in'
  const oneTimeForm = route === 'setup' || route === 'join' || route === 'reset'

  useEffect(() => {
    if (!page) return
    if (page.Lang) document.documentElement.lang = page.Lang
    if (page.Heading) document.title = page.Heading + ' · Beacon'
    if (/^#[a-z\d_-]+$/i.test(hash)) document.getElementById(hash.slice(1))?.scrollIntoView()
  }, [page, hash])

  if (resource?.error || page?.error) return <Empty>{t('loadError')}</Empty>
  if (!page) return <Loading t={t} />

  function finish(result?: ViewResult<unknown>) {
    if (result?.Redirect) {
      const next = new URL(result.Redirect, location.origin)
      if (next.pathname === '/console/sign-in' || signIn || oneTimeForm) {
        location.assign(next.pathname === '/console/sign-in' ? '/workspace/sign-in' + next.search : result.Redirect)
        return
      }
      setSubmitted(undefined)
      setRevision(current => current + 1)
      visit(result.Redirect)
    } else {
      setSubmitted(result)
    }
  }

  return <AdminContext.Provider value={{ csrf, t, finish }}>
    {signIn ? <SignInForm data={page.Data as SignInData} /> : oneTimeForm ? <AccountLinkForm data={page.Data as OneTimeForm} /> :
      <>
        <PageHeading title={page.Heading} actions={page.Kind !== 'once' && page.Crumbs?.slice(1).filter(crumb => crumb.Href).map(crumb =>
          <Link key={crumb.Href} className="button" href={crumb.Href}>{crumb.Label}</Link>)} />
        {page.Problem && <div className="notice operation-error" role="alert">{page.Problem}</div>}
        {page.Toast && <div className="notice operation-result" role="status">{page.Toast}</div>}
        {page.Kind === 'once' ? <Once data={page.Data as OnceData} /> :
          page.Kind === 'message' ? <p role="alert">{(page.Data as { Text: string }).Text}</p> : pageBody(route, page.Data)}
      </>}
  </AdminContext.Provider>
}
