export type Tone = 'danger' | 'warning' | 'success' | 'info' | ''
export type Label = (key: string) => string
export type LinkTarget = { K: string; V: string; Href: string }

export type NavItem = {
  Key: string
  Label: string
  Href: string
  Icon: string
  Count: string
}

export type WorkspaceSession = {
  language: string
  name: string
  whoMeta: string
  whoScopes: string
  csrf: string
  docs: string
  repo: string
  issues: string
  version: string
  mcpEndpoint: string
  openApiHref: string
  homeHref: string
  sshChannel: { Text: string; Tone: Tone; Href: string }
  labels: Record<string, string>
  navigation: { Label: string; Items: NavItem[] }[]
}

export type CapabilitiesData = {
  caller: {
    actor: string
    user?: string
    agent?: string
    preset?: string
    scopes: string[]
    machine_scope: string
    machines?: string[]
    person: boolean
    standalone: boolean
  }
  features: {
    ssh: boolean
    ssh_paused: boolean
    config: boolean
    status: boolean
    probe_now: boolean
    team_credentials: boolean
    share_links: boolean
    mcp_endpoint?: string
    openapi_url?: string
    docs_url?: string
  }
  domains: {
    id: string
    title: string
    tag: string
    summary: string
    workflow: string
    operations: {
      tool: string
      rest: string
      domain: string
      access: 'read' | 'write' | 'destructive'
      scopes: string[]
      scope_mode?: string
      summary: string
      available: boolean
      unavailable_reason?: string
    }[]
  }[]
  resources: {
    uri: string
    template?: boolean
    name: string
    title: string
    domain: string
    scopes: string[]
    description: string
    available: boolean
  }[]
  prompts: {
    name: string
    title: string
    domain: string
    description: string
    arguments?: { name: string; description: string; required: boolean }[]
  }[]
}

export type Metric = {
  Label: string; Value: string; Meta: string; Href: string; Tone: Tone
  Coverage: { Reporting: number; Expected: number } | null
}

type QueueItem = {
  Title: string; Href: string; Sub: string; Badge: string; BadgeTone: Tone; When: string
  Response: string; DeviceResponse: string; Silenced: boolean; Unclaimed: boolean
}

type Notification = {
  Channel: string; Schedule: string; Result: string; Tone: Tone; At: string
  Href: string; OpenLabel: string; DeviceAcknowledgement: string
}

export type FeedItem = {
  Title: string; Href: string; Badge: string; At?: string; Sub: string; When: string; Tone: Tone
}

export type OverviewData = {
  health: { Title: string; Text: string; Tone: Tone }
  metrics: { Firing: Metric; Services: Metric; Locations: Metric; Machines: Metric; Sessions: Metric }
  queue: QueueItem[] | null
  notifications: Notification[] | null
  deliveryHint: string
  deliveryHelp: LinkTarget
  deliveryDrills: LinkTarget[] | null
  maintenance: LinkTarget
  upcoming: FeedItem[] | null
  sessions: FeedItem[] | null
  changes: FeedItem[] | null
  addMonitor: LinkTarget
}

type Filter = { Label: string; Href: string; Count?: string; On?: boolean }
type AlertRow = {
  Title: string; Href: string; Severity: string; SeverityTone: Tone; State: string; StateTone: Tone
  Service: string; Since: string; When: string; Acked: boolean; Silenced: boolean; DeviceResponse: string
}

export type AlertData = {
  alerts: AlertRow[] | null
  views: Filter[]
  list: {
    Path: string; Query: string; SearchHint: string; On: number
    Hidden: { K: string; V: string }[] | null
    Tags: Filter[] | null
    Facets: { Title: string; Clear: string; Values: Filter[]; More: string }[] | null
    Pager: { Summary: string; Prev: string; Next: string }
    Empty: string
  }
}

type Probe = {
  Location: string; Instance: string; Module: string; State: string; Reason: string
  Detail: string; Since: string; Tone: Tone; OK: boolean
}

type ProbeEvent = { Summary: string; RecoveryHint: string; Open: boolean; Rows: Probe[] | null }

export type Incident =
  | { archived: string }
  | {
    ID: string; Episode: string; Title: string; Description: string; Status: string; Severity: string
    StatusTone: Tone; SeverityTone: Tone; When: string; Ownership: string; Escalation: string
    Phone: string; Delivery: string; Hook: string; ProbesError: string; Window: string; Legacy: string
    Actions: { TakeOver: boolean; CanAck: boolean; CanNote: boolean; CanProbe: boolean; AckVersion: string }
    Machines: { Title: string; Hint: string; Items: LinkTarget[] | null } | null
    Events: { At: string; ISO: string; Kind: string; Text: string; Detail: string; Href: string; HrefText: string; Anchor: string; Probe: ProbeEvent | null }[] | null
    Probes: Probe[] | null
    Sessions: { ID: string; Agent: string; Machine: string; Started: string; Kind: string; Href: string }[] | null
    Notes: { By: string; At: string; ISO: string; Text: string }[] | null
    LatestCheck: ProbeEvent | null
    Detail: {
      Labels: Record<string, string>; Services: string[] | null; Agents: string[] | null; Channel: string; Paused: boolean
      Related: { Title: string; Href: string; Severity: string; SeverityClass: Tone }[] | null
      Changes: { At: string; Source: string; Kind: string; Summary: string; About: string; Href: string }[] | null
      Runbook: string; Dashboard: string; Share: string; Feedback: string; FeedbackTone: Tone
      PhoneCancellation: string; PhonePending: boolean; LatestProbeHref: string
      Handover: { Preview: string; Full: string }
      AgentRun: { Title: string; Status: string; Tone: Tone; Exit: string; Anchor: string } | null
      Actions: { Silence: string; Unsilence: string; Back: string; Episode: string; ReturnEpisode: string; Durations: { K: string; V: string }[] | null } | null
    }
  }

export class APIError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) {
    super(message)
  }
}

const root = '/console/api/v1'

export async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(root + path, {
    ...options,
    credentials: 'same-origin',
    headers: { Accept: 'application/json', ...options?.headers },
  })
  if (!response.ok) {
    if (response.status === 401 && path !== '/session') {
      const next = location.pathname + location.search + location.hash
      location.assign('/workspace/sign-in?next=' + encodeURIComponent(next))
    }
    const data: { code?: string; error?: string } = await response.json().catch(() => ({}))
    throw new APIError(response.status, data.code ?? '', data.error ?? response.statusText)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export function incidentAction(id: string, action: 'ack' | 'notes' | 'probe-now', csrf: string, payload: {
  episode: string; ackVersion?: string; text?: string
}) {
  return request<void>(`/alerts/${encodeURIComponent(id)}/${action}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
    body: JSON.stringify(payload),
  })
}
