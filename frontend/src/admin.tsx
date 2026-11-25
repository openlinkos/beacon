import { createContext, useContext, useEffect, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import type { Label, Tone } from './api'
import { Signal } from './components'
import { Link, navigate, workspaceHref } from './navigation'

export type Pick = { Value: string; Label: string; Help: string; On: boolean; Selected?: boolean; Disabled?: boolean }
export type Pair = { K: string; V: string; Href?: string }
export type ViewResult<T> = {
  Kind: string; Heading: string; Count: string; Problem: string; Toast: string; ToastTone: Tone; Lang: string
  Crumbs: { Label: string; Href: string }[] | null
  Data: T; Redirect?: string; error?: string
}

export const AdminContext = createContext<{
  csrf: string; t: Label; finish: (next?: ViewResult<unknown>) => void
} | null>(null)

export function useAdmin() {
  const context = useContext(AdminContext)
  if (!context) throw new Error('The form needs a workspace route.')
  return context
}

const prefix = '/console/api/v1/page'

function viewURL(path: string) {
  const url = new URL(path, location.origin)
  if (url.origin !== location.origin || !url.pathname.startsWith('/console/')) throw new Error('Invalid console route.')
  return prefix + url.pathname.slice('/console'.length) + url.search
}

export function visit(path: string) {
  const destination = workspaceHref(path)
  if (destination.startsWith('/workspace/')) navigate(destination)
  else location.assign(destination)
}

export function useView<T>(path: string, revision = 0) {
  const [result, setResult] = useState<{ path: string; page?: ViewResult<T>; error?: boolean }>()
  useEffect(() => {
    const controller = new AbortController()
    fetch(viewURL(path), { headers: { Accept: 'application/json' }, signal: controller.signal }).then(async response => {
      const page: ViewResult<T> = await response.json()
      if (controller.signal.aborted) return
      if (page.Redirect) visit(page.Redirect)
      else setResult({ path, page })
    }).catch(() => {
      if (!controller.signal.aborted) setResult({ path, error: true })
    })
    return () => controller.abort()
  }, [path, revision])
  return result?.path === path ? result : undefined
}

export function Section({ title, actions, children }: { title: string; actions?: ReactNode; children?: ReactNode }) {
  return <section className="admin-section">
    <header className="section-heading"><h2>{title}</h2>{actions}</header>
    <div className="admin-content">{children}</div>
  </section>
}

export function Facts({ rows }: { rows?: Pair[] | null }) {
  if (!rows?.length) return null
  return <dl className="facts">{rows.map((row, index) => <div key={index}><dt>{row.K}</dt><dd>
    {row.Href ? <Link href={row.Href}>{row.V}</Link> : row.V}
  </dd></div>)}</dl>
}

export function Table({ columns, rows, empty }: { columns: string[]; rows: ReactNode[][]; empty?: string }) {
  if (!rows.length) return empty ? <p className="empty">{empty}</p> : null
  return <table className="resource-table">
    <thead><tr>{columns.map((column, index) => <th key={index} scope="col">{column}</th>)}</tr></thead>
    <tbody>{rows.map((row, index) => <tr key={index}>{row.map((cell, i) => <td key={i} data-label={i ? columns[i] : undefined}>{cell}</td>)}</tr>)}</tbody>
  </table>
}

export function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="field"><span>{label}</span>{children}</label>
}

export function Select({ name, options, initial }: { name: string; options?: Pick[] | null; initial?: string }) {
  return <select name={name} defaultValue={initial ?? options?.find(option => option.On || option.Selected)?.Value}>
    {options?.map(option => <option disabled={option.Disabled} key={option.Value} value={option.Value}>{option.Label}</option>)}
  </select>
}

export function ToneText({ text, tone }: { text: string; tone?: Tone }) {
  return text ? <Signal tone={tone}>{text}</Signal> : null
}

export function Action({ action, label, children, danger, compact, primary, hidden, intent, edit }: {
  action: string; label: string; children?: ReactNode; danger?: boolean; compact?: boolean; primary?: boolean
  hidden?: Record<string, string | number | undefined>
  intent?: string; edit?: string
}) {
  const { csrf, t, finish } = useAdmin()
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget, (event.nativeEvent as SubmitEvent).submitter)
    if (csrf && !form.has('csrf')) form.set('csrf', csrf)
    const fields = new URLSearchParams()
    for (const [name, value] of form) if (typeof value === 'string') fields.append(name, value)
    setPending(true)
    setError('')
    try {
      const response = await fetch(viewURL(action), { method: 'POST', headers: { Accept: 'application/json' }, body: fields })
      const result: ViewResult<unknown> = await response.json()
      if (result.Redirect) finish(result)
      else if (response.ok || result.Data) finish(result)
      else setError(result.error || t('failed'))
    } catch {
      setError(t('failed'))
    } finally {
      setPending(false)
    }
  }

  return <form className={compact ? 'compact-action' : 'admin-form'} onSubmit={submit} aria-busy={pending}>
    {Object.entries(hidden ?? {}).map(([name, value]) => value !== undefined && <input key={name} type="hidden" name={name} value={value} readOnly />)}
    {children}
    <div className="form-actions">
      {edit && <button type="submit" name="intent" value="edit" className="button" disabled={pending}>{edit}</button>}
      <button type="submit" name={intent ? 'intent' : undefined} value={intent} className={`button${danger ? ' danger' : ''}${primary ? ' primary' : ''}`} disabled={pending}>{label}</button>
    </div>
    {error && <p className="operation-error" role="alert">{error}</p>}
  </form>
}

export function Discovery({ action, search, value }: { action: string; search: string; value?: string }) {
  return <form className="resource-search" role="search" key={value ?? ''} onSubmit={event => {
    event.preventDefault()
    const q = new FormData(event.currentTarget).get('q')
    visit(action + (typeof q === 'string' && q.trim() ? '?q=' + encodeURIComponent(q.trim()) : ''))
  }}>
    <input aria-label={search} type="search" name="q" placeholder={search} defaultValue={value} />
    <button className="button">{search}</button>
  </form>
}

export function Pagination({ pager }: { pager?: { Summary: string; Prev: string; Next: string } }) {
  const { t } = useAdmin()
  if (!pager?.Summary) return null
  return <nav className="pager" aria-label={pager.Summary}>
    <span>{pager.Summary}</span>
    {pager.Prev && <Link href={pager.Prev}>{t('prev')}</Link>}
    {pager.Next && <Link href={pager.Next}>{t('next')}</Link>}
  </nav>
}
