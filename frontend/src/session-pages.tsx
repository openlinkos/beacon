import { useEffect, useRef, useState } from 'react'
import { Facts, Pagination, Section, Table, ToneText } from './admin'
import { Link } from './navigation'
import { visit } from './admin'
import { SessionRows } from './machine-pages'
import type { SessionRecord } from './machine-pages'
import type { Tone } from './api'

type Text = Record<string, string>
type ReplayPlayer = { dispose?: () => void; addEventListener?: (event: string, listener: () => void) => void }
type ReplayLibrary = { create: (src: string, el: HTMLElement, options: object) => ReplayPlayer }
let playerLibrary: Promise<ReplayLibrary> | undefined

function loadPlayer() {
  if (playerLibrary) return playerLibrary
  playerLibrary = new Promise((resolve, reject) => {
    const stylesheet = document.createElement('link')
    stylesheet.rel = 'stylesheet'
    stylesheet.href = '/static/asciinema-player.css'
    document.head.append(stylesheet)
    const script = document.createElement('script')
    script.src = '/static/asciinema-player.min.js'
    script.onload = () => {
      const player = (window as unknown as { AsciinemaPlayer?: ReplayLibrary }).AsciinemaPlayer
      if (player) resolve(player)
      else reject(new Error('The playback library did not load.'))
    }
    script.onerror = () => reject(new Error('The playback library did not load.'))
    document.head.append(script)
  })
  return playerLibrary
}

function Replay({ src, failure }: { src: string; failure: string }) {
  const container = useRef<HTMLDivElement>(null)
  const [error, setError] = useState(false)
  useEffect(() => {
    let disposed = false
    let instance: ReplayPlayer | undefined
    const element = container.current
    loadPlayer().then(library => {
      if (disposed || !element) return
      instance = library.create(src, element, { fit: 'both', preload: true, idleTimeLimit: 2, controls: true })
      instance.addEventListener?.('error', () => { if (!disposed) setError(true) })
    }).catch(() => { if (!disposed) setError(true) })
    return () => {
      disposed = true
      instance?.dispose?.()
      element?.replaceChildren()
    }
  }, [src])
  return <><div className="replay" ref={container} />{error && <p role="alert">{failure}</p>}</>
}

export type SessionsData = {
  L: Text; Rows: SessionRecord[] | null; Details: boolean
  List: {
    SearchLabel: string; Query: string; Empty: string
    Tags: { Label: string; Href: string }[] | null
    Facets: { Title: string; Values: { Label: string; Href: string; On: boolean; Count: string }[] | null }[] | null
    Pager: { Summary: string; Prev: string; Next: string }
  }
}

export function Sessions({ data: p }: { data: SessionsData }) {
  const list = p.List
  return <>
    <div className="resource-tools">
      <form role="search" className="resource-search" key={list.Query} onSubmit={event => {
        event.preventDefault()
        const query = new URLSearchParams(location.search)
        const value = new FormData(event.currentTarget).get('q')
        query.delete('page')
        if (typeof value === 'string' && value.trim()) query.set('q', value.trim())
        else query.delete('q')
        visit('/console/sessions?' + query)
      }}>
        <input type="search" name="q" aria-label={list.SearchLabel} placeholder={list.SearchLabel} defaultValue={list.Query} />
        <button className="button">{list.SearchLabel}</button>
      </form>
      {list.Facets?.map(facet => <details className="disclosure" key={facet.Title}><summary>{facet.Title}</summary>
        <ul className="plain-list">{facet.Values?.map(value => <li key={value.Href}><Link href={value.Href} aria-current={value.On ? 'true' : undefined}>{value.Label} {value.Count}</Link></li>)}</ul>
      </details>)}
    </div>
    {list.Tags?.length ? <div className="filter-tags">{list.Tags.map(tag => <Link key={tag.Href} href={tag.Href}>{tag.Label} ×</Link>)}</div> : null}
    <SessionRows rows={p.Rows} l={p.L} details={p.Details} empty={list.Empty} />
    <Pagination pager={list.Pager} />
  </>
}

export type SessionData = {
  L: Text; ID: string; ActorLabel: string; Agent: string; Machine: string; MachineHref: string; Kind: string
  Started: string; Ended: string; Duration: string; Exit: string; Via: string; Command: string
  Recording: string; RecordingNote: string; RecordingReadNote: string; TranscriptNote: string; ShareLinks: string
  RecordingState: { Text: string; Tone: Tone }; TranscriptState: { Text: string; Tone: Tone }
  Commands: { Command: string; Exit: string; Took: string; Failed: boolean }[] | null
  Outputs: { K: string; V: string }[] | null
}

export function Session({ data: p }: { data: SessionData }) {
  const l = p.L
  return <>
    <div className="session-facts"><Facts rows={[
      { K: p.ActorLabel, V: p.Agent }, { K: l.Machine, V: p.Machine, Href: p.MachineHref }, { K: l.Kind, V: p.Kind },
      { K: l.Via, V: p.Via }, { K: l.Started, V: p.Started }, { K: l.Ended, V: p.Ended },
      { K: l.Duration, V: p.Duration }, { K: l.Exit, V: p.Exit },
    ].filter(row => row.V)} /></div>
    <Section title={l.Recording} actions={<div className="resource-tools">
      {p.Recording && <a className="button" href={p.Recording} download={p.ID + '.cast'}>{l.Download}</a>}
      {p.ShareLinks && <Link className="button" href={p.ShareLinks}>{l.ShareLinks}</Link>}
    </div>}>
      <ToneText text={p.RecordingState.Text} tone={p.RecordingState.Tone} />
      {(p.RecordingReadNote || p.RecordingNote) && <p role="status">{p.RecordingReadNote || p.RecordingNote}</p>}
      {p.Recording && <Replay key={p.Recording} src={p.Recording} failure={l.RecordingError} />}
    </Section>
    <Section title={l.Transcript}>
      <ToneText text={p.TranscriptState.Text} tone={p.TranscriptState.Tone} />
      {p.TranscriptNote && <p role="status">{p.TranscriptNote}</p>}
      {p.Command && <pre>{p.Command}</pre>}
      <Table columns={[l.Commands, l.Exit, l.Duration]} rows={(p.Commands ?? []).map(row => [
        <code>{row.Command}</code>, <ToneText text={row.Exit} tone={row.Failed ? 'danger' : ''} />, row.Took,
      ])} />
      {p.Outputs?.map(out => <div key={out.K}><h3>{out.K}</h3><pre>{out.V}</pre></div>)}
    </Section>
  </>
}
