import { useState } from 'react'
import type { CapabilitiesData, Label, WorkspaceSession } from './api'
import { CopyBlock, Empty, Loading, PageHeading, Signal } from './components'
import { Icon } from './icons'
import { useResource } from './resources'

const domainIcons: Record<string, string> = {
  incidents: 'triangle-alert',
  monitoring: 'activity',
  machines: 'terminal',
  status: 'globe',
  identity: 'users',
  governance: 'shield-check',
}

function accessTone(access: string): 'info' | 'warning' | 'danger' {
  if (access === 'destructive') return 'danger'
  if (access === 'write') return 'warning'
  return 'info'
}

function accessLabel(access: string, t: Label): string {
  if (access === 'destructive') return t('accessDestructive')
  if (access === 'write') return t('accessWrite')
  return t('accessRead')
}

function splitRest(rest: string): [string, string] {
  const space = rest.indexOf(' ')
  if (space < 0) return ['', rest]
  return [rest.slice(0, space), rest.slice(space + 1)]
}

export function CapabilitiesHub({ session, t }: { session: WorkspaceSession; t: Label }) {
  const resource = useResource<CapabilitiesData>('/capabilities')
  const [activeDomain, setActiveDomain] = useState('all')
  const [query, setQuery] = useState('')
  const [availableOnly, setAvailableOnly] = useState(false)

  if (resource.error) return <Empty icon="triangle-alert">{t('loadError')}</Empty>
  const data = resource.data
  if (!data) return <Loading t={t} />

  const mcpURL = data.features.mcp_endpoint || session.mcpEndpoint
  const apiBase = mcpURL.replace(/\/mcp$/, '')
  const openApiURL = data.features.openapi_url || session.openApiHref
  const q = query.trim().toLowerCase()

  const totalOps = data.domains.reduce((sum, d) => sum + d.operations.length, 0)
  const availableOps = data.domains.reduce((sum, d) => sum + d.operations.filter(o => o.available).length, 0)

  const visibleDomains = data.domains
    .filter(d => activeDomain === 'all' || d.id === activeDomain)
    .map(d => ({
      ...d,
      operations: d.operations.filter(op => {
        if (availableOnly && !op.available) return false
        if (!q) return true
        return (
          op.tool.toLowerCase().includes(q) ||
          op.rest.toLowerCase().includes(q) ||
          op.summary.toLowerCase().includes(q) ||
          op.scopes.some(s => s.toLowerCase().includes(q)) ||
          d.title.toLowerCase().includes(q)
        )
      }),
    }))
    .filter(d => d.operations.length > 0 || (!q && activeDomain === d.id))

  const visibleResources = data.resources.filter(r => {
    if (activeDomain !== 'all' && r.domain !== activeDomain) return false
    if (availableOnly && !r.available) return false
    if (!q) return true
    return r.uri.toLowerCase().includes(q) || r.title.toLowerCase().includes(q) || r.description.toLowerCase().includes(q)
  })

  const visiblePrompts = data.prompts.filter(p => {
    if (activeDomain !== 'all' && p.domain !== activeDomain) return false
    if (!q) return true
    return p.name.toLowerCase().includes(q) || p.title.toLowerCase().includes(q) || p.description.toLowerCase().includes(q)
  })

  const featureBadges: { label: string; on: boolean; warn?: boolean }[] = [
    { label: data.features.ssh_paused ? 'SSH (Paused)' : 'SSH', on: data.features.ssh, warn: data.features.ssh_paused },
    { label: 'Probe Now', on: data.features.probe_now },
    { label: 'Status Pages', on: data.features.status },
    { label: 'Config DB', on: data.features.config },
    { label: 'Share Links', on: data.features.share_links },
    { label: 'Team Credentials', on: data.features.team_credentials },
  ]

  // all: every machine that is not critical, and the critical machines named beside it
  const machineScopeLabel = data.caller.machine_scope === 'all'
    ? [t('allMachines'), ...(data.caller.machines ?? [])].join(' + ')
    : data.caller.machine_scope === 'none'
      ? t('noMachines')
      : (data.caller.machines ?? []).join(', ')

  return (
    <div className="hub-stack">
      <PageHeading
        title={t('mcpHub')}
        subtitle={t('mcpHubSubtitle')}
        badge={<Signal tone="info">{`${availableOps}/${totalOps} ${t('tools')}`}</Signal>}
        actions={
          <>
            <a className="button" href={openApiURL} target="_blank" rel="noopener">
              <Icon name="external-link" size="xs" />
              <span>{t('openApiSpec')}</span>
            </a>
            <a className="button" href={session.docs} target="_blank" rel="noopener">
              <Icon name="book-open" size="xs" />
              <span>{t('docs')}</span>
            </a>
          </>
        }
      />

      <div className="hub-top-grid">
        <section className="hub-card">
          <div className="hub-card-head">
            <span className="hub-card-title"><Icon name="bot" size="sm" /><h2>{t('connectAgent')}</h2></span>
            <code className="endpoint-pill">{mcpURL}</code>
          </div>
          <div className="hub-snippets">
            <CopyBlock
              title="Claude Code"
              label="MCP + OAuth 2.1"
              code={`claude mcp add --transport http beacon ${mcpURL}`}
              copyLabel={t('copy')}
              copiedLabel={t('copied')}
            />
            <CopyBlock
              title="Codex"
              label="MCP + OAuth 2.1"
              code={`codex mcp add beacon --url ${mcpURL}`}
              copyLabel={t('copy')}
              copiedLabel={t('copied')}
            />
            <CopyBlock
              title="REST Discovery"
              label="GET /api/v1/capabilities"
              code={`curl -H "Authorization: Bearer <token>" ${apiBase}/api/v1/capabilities`}
              copyLabel={t('copy')}
              copiedLabel={t('copied')}
            />
          </div>
        </section>

        <section className="hub-card hub-identity-card">
          <div className="hub-card-head">
            <span className="hub-card-title"><Icon name="shield-check" size="sm" /><h2>{t('callerIdentity')}</h2></span>
            <Signal tone="success">{data.caller.actor}</Signal>
          </div>
          <div className="hub-identity-meta">
            <div>
              <span className="meta-label">{t('machineScope')}</span>
              <strong>{machineScopeLabel}</strong>
            </div>
            <div>
              <span className="meta-label">{t('domains')}</span>
              <strong>{data.domains.length}</strong>
            </div>
            <div>
              <span className="meta-label">{t('resources')}</span>
              <strong>{data.resources.length}</strong>
            </div>
            <div>
              <span className="meta-label">{t('prompts')}</span>
              <strong>{data.prompts.length}</strong>
            </div>
          </div>
          <div className="hub-meta-block">
            <span className="meta-label">{t('activeScopes')}</span>
            <div className="pill-row">
              {data.caller.scopes.map(scope => <code key={scope} className="scope-pill">{scope}</code>)}
            </div>
          </div>
          <div className="hub-meta-block">
            <span className="meta-label">{t('enabledFeatures')}</span>
            <div className="pill-row">
              {featureBadges.map(f => (
                <Signal key={f.label} tone={f.warn ? 'danger' : f.on ? 'success' : ''}>
                  {f.label}
                </Signal>
              ))}
            </div>
          </div>
        </section>
      </div>

      <section className="hub-explorer">
        <div className="hub-toolbar">
          <div className="domain-tabs" role="tablist" aria-label={t('domains')}>
            <button
              type="button"
              role="tab"
              aria-selected={activeDomain === 'all'}
              className={`domain-tab${activeDomain === 'all' ? ' is-active' : ''}`}
              onClick={() => setActiveDomain('all')}
            >
              <Icon name="layers" size="xs" />
              <span>{t('allDomains')}</span>
              <span className="tab-count">{totalOps}</span>
            </button>
            {data.domains.map(d => (
              <button
                key={d.id}
                type="button"
                role="tab"
                aria-selected={activeDomain === d.id}
                className={`domain-tab${activeDomain === d.id ? ' is-active' : ''}`}
                onClick={() => setActiveDomain(d.id)}
              >
                <Icon name={domainIcons[d.id] || 'layers'} size="xs" />
                <span>{d.title}</span>
                <span className="tab-count">{d.operations.length}</span>
              </button>
            ))}
          </div>
          <div className="hub-filters">
            <div className="search-input-wrap">
              <Icon name="search" size="xs" />
              <input
                type="search"
                value={query}
                onChange={e => setQuery(e.target.value)}
                placeholder={t('search')}
                aria-label={t('search')}
              />
            </div>
            <label className="toggle-pill">
              <input type="checkbox" checked={availableOnly} onChange={e => setAvailableOnly(e.target.checked)} />
              <span>{t('availableOnly')}</span>
            </label>
          </div>
        </div>

        <div className="domain-list">
          {visibleDomains.length ? visibleDomains.map(d => (
            <article className="domain-card" key={d.id}>
              <header className="domain-head">
                <div className="domain-title-wrap">
                  <span className="domain-icon"><Icon name={domainIcons[d.id] || 'layers'} size="sm" /></span>
                  <div>
                    <div className="domain-title-line">
                      <h3>{d.title}</h3>
                      <span className="code-card-tag">{d.tag}</span>
                    </div>
                    <p>{d.summary}</p>
                  </div>
                </div>
              </header>
              {d.workflow && (
                <div className="workflow-callout">
                  <Icon name="sparkles" size="xs" />
                  <span><strong>{t('workflow')}:</strong> {d.workflow}</span>
                </div>
              )}
              {d.operations.length ? (
                <div className="table-scroll">
                  <table className="ops-table">
                    <thead>
                      <tr>
                        <th scope="col">{t('mcpTool')}</th>
                        <th scope="col">{t('restEndpoint')}</th>
                        <th scope="col">{t('state')}</th>
                        <th scope="col">{t('activeScopes')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {d.operations.map(op => {
                        const [method, path] = splitRest(op.rest)
                        return (
                          <tr key={op.tool} className={op.available ? '' : 'is-restricted'}>
                            <td>
                              <div className="op-tool-cell">
                                <code className="tool-name">{op.tool}</code>
                                <Signal tone={accessTone(op.access)}>{accessLabel(op.access, t)}</Signal>
                              </div>
                              <p className="op-summary">{op.summary}</p>
                            </td>
                            <td>
                              {path ? (
                                <div className="rest-cell">
                                  {method && <span className="http-method" data-method={method}>{method}</span>}
                                  <code>{path}</code>
                                </div>
                              ) : <span className="muted-dash">—</span>}
                            </td>
                            <td>
                              <Signal tone={op.available ? 'success' : 'warning'}>
                                {op.available ? t('available') : t('restricted')}
                              </Signal>
                              {!op.available && op.unavailable_reason && <small className="op-reason">{op.unavailable_reason}</small>}
                            </td>
                            <td>
                              <div className="pill-row">
                                {op.scopes.length ? op.scopes.map(sc => <code key={sc} className="scope-pill">{sc}</code>) : <span className="muted-dash">—</span>}
                              </div>
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              ) : <Empty icon="search">{t('noMatches')}</Empty>}
            </article>
          )) : <Empty icon="search">{t('noMatches')}</Empty>}
        </div>
      </section>

      <div className="hub-bottom-grid">
        <section className="hub-card">
          <div className="hub-card-head">
            <span className="hub-card-title"><Icon name="radio" size="sm" /><h2>{t('resources')}</h2></span>
            <span className="tab-count">{visibleResources.length}</span>
          </div>
          {visibleResources.length ? (
            <ul className="spec-list">
              {visibleResources.map(res => (
                <li key={res.uri} className="spec-item">
                  <div className="spec-head">
                    <code className="uri-code">{res.uri}</code>
                    <Signal tone={res.available ? 'success' : 'warning'}>{res.available ? t('available') : t('restricted')}</Signal>
                  </div>
                  <strong>{res.title}</strong>
                  <p>{res.description}</p>
                </li>
              ))}
            </ul>
          ) : <Empty icon="radio">{t('noMatches')}</Empty>}
        </section>

        <section className="hub-card">
          <div className="hub-card-head">
            <span className="hub-card-title"><Icon name="sparkles" size="sm" /><h2>{t('prompts')}</h2></span>
            <span className="tab-count">{visiblePrompts.length}</span>
          </div>
          {visiblePrompts.length ? (
            <ul className="spec-list">
              {visiblePrompts.map(prompt => (
                <li key={prompt.name} className="spec-item">
                  <div className="spec-head">
                    <code className="tool-name">{prompt.name}</code>
                    <span className="code-card-tag">{prompt.domain}</span>
                  </div>
                  <strong>{prompt.title}</strong>
                  <p>{prompt.description}</p>
                  {prompt.arguments?.length ? (
                    <div className="pill-row">
                      {prompt.arguments.map(arg => (
                        <code key={arg.name} className="scope-pill">
                          {arg.name}{arg.required ? '*' : '?'}
                        </code>
                      ))}
                    </div>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : <Empty icon="sparkles">{t('noMatches')}</Empty>}
        </section>
      </div>
    </div>
  )
}
