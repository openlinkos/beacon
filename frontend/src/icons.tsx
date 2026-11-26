import type { ReactNode } from 'react'

type IconProps = { name: string; size?: 'xs' | 'sm' | 'md' | 'lg'; className?: string }

const paths: Record<string, ReactNode> = {
  'layout-dashboard': <>
    <rect x="3" y="3" width="7" height="9" rx="1.5" />
    <rect x="14" y="3" width="7" height="5" rx="1.5" />
    <rect x="14" y="12" width="7" height="9" rx="1.5" />
    <rect x="3" y="16" width="7" height="5" rx="1.5" />
  </>,
  'triangle-alert': <>
    <path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z" />
    <path d="M12 9v4" />
    <path d="M12 17h.01" />
  </>,
  'clock': <>
    <circle cx="12" cy="12" r="9" />
    <polyline points="12 7 12 12 15.5 14" />
  </>,
  'zap': <polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" />,
  'history': <>
    <path d="M3 12a9 9 0 1 0 3-6.7L3 8" />
    <path d="M3 3v5h5" />
    <path d="M12 7v5l3 2" />
  </>,
  'activity': <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />,
  'timer': <>
    <line x1="10" x2="14" y1="2" y2="2" />
    <line x1="12" x2="15" y1="14" y2="11" />
    <circle cx="12" cy="14" r="8" />
  </>,
  'globe': <>
    <circle cx="12" cy="12" r="9.5" />
    <path d="M2.5 12h19" />
    <path d="M12 2.5a14.5 14.5 0 0 1 0 19 14.5 14.5 0 0 1 0-19" />
  </>,
  'server': <>
    <rect x="3" y="3" width="18" height="7.5" rx="2" />
    <rect x="3" y="13.5" width="18" height="7.5" rx="2" />
    <line x1="7" x2="7.01" y1="6.75" y2="6.75" />
    <line x1="7" x2="7.01" y1="17.25" y2="17.25" />
  </>,
  'terminal': <>
    <polyline points="4 17 10 11 4 5" />
    <line x1="12" x2="20" y1="19" y2="19" />
  </>,
  'bot': <>
    <rect x="4" y="8" width="16" height="12" rx="3" />
    <path d="M12 8V4H9" />
    <line x1="9" x2="9.01" y1="14" y2="14" />
    <line x1="15" x2="15.01" y1="14" y2="14" />
    <path d="M2 14h2" />
    <path d="M20 14h2" />
  </>,
  'users': <>
    <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
    <circle cx="9" cy="7" r="4" />
    <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
    <path d="M16 3.13a4 4 0 0 1 0 7.75" />
  </>,
  'file-cog': <>
    <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z" />
    <polyline points="14 2 14 8 20 8" />
    <circle cx="12" cy="15" r="2.5" />
  </>,
  'key-round': <>
    <path d="M2.5 18.5v-3a1 1 0 0 1 .3-.7l6.5-6.5a5.5 5.5 0 1 1 3.9 3.9l-1.7 1.7H9.5v2H7.5v2H4a1.5 1.5 0 0 1-1.5-1.4Z" />
    <circle cx="16.5" cy="7.5" r="1" />
  </>,
  'scroll-text': <>
    <path d="M8 21h10a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2Z" />
    <path d="M10 8h6" />
    <path d="M10 12h6" />
    <path d="M10 16h4" />
    <path d="M4 7v11a3 3 0 0 0 3 3" />
  </>,
  'layers': <>
    <polygon points="12 2 2 7 12 12 22 7 12 2" />
    <polyline points="2 12 12 17 22 12" />
    <polyline points="2 17 12 22 22 17" />
  </>,
  'shield-check': <>
    <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z" />
    <path d="m9 12 2 2 4-4" />
  </>,
  'search': <>
    <circle cx="11" cy="11" r="7" />
    <line x1="21" x2="16.65" y1="21" y2="16.65" />
  </>,
  'sun': <>
    <circle cx="12" cy="12" r="4" />
    <path d="M12 2v2" />
    <path d="M12 20v2" />
    <path d="m4.93 4.93 1.41 1.41" />
    <path d="m17.66 17.66 1.41 1.41" />
    <path d="M2 12h2" />
    <path d="M20 12h2" />
    <path d="m6.34 17.66-1.41 1.41" />
    <path d="m19.07 4.93-1.41 1.41" />
  </>,
  'moon': <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z" />,
  'monitor': <>
    <rect x="2" y="3" width="20" height="14" rx="2" />
    <line x1="8" x2="16" y1="21" y2="21" />
    <line x1="12" x2="12" y1="17" y2="21" />
  </>,
  'copy': <>
    <rect x="9" y="9" width="12" height="12" rx="2" />
    <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
  </>,
  'check': <polyline points="20 6 9 17 4 12" />,
  'check-circle': <>
    <circle cx="12" cy="12" r="9.5" />
    <path d="m8.5 12.2 2.3 2.3 4.7-4.8" />
  </>,
  'command': <path d="M15 6v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3V6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3" />,
  'external-link': <>
    <path d="M15 3h6v6" />
    <path d="M10 14 21 3" />
    <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
  </>,
  'arrow-up-right': <>
    <path d="M7 17 17 7" />
    <path d="M7 7h10v10" />
  </>,
  'log-out': <>
    <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
    <polyline points="16 17 21 12 16 7" />
    <line x1="21" x2="9" y1="12" y2="12" />
  </>,
  'log-in': <>
    <path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4" />
    <polyline points="10 17 15 12 10 7" />
    <line x1="15" x2="3" y1="12" y2="12" />
  </>,
  'plus': <>
    <line x1="12" x2="12" y1="5" y2="19" />
    <line x1="5" x2="19" y1="12" y2="12" />
  </>,
  'refresh-cw': <>
    <path d="M21 12a9 9 0 0 0-9-9 9.75 9.75 0 0 0-6.74 2.74L3 8" />
    <path d="M3 3v5h5" />
    <path d="M3 12a9 9 0 0 0 9 9 9.75 9.75 0 0 0 6.74-2.74L21 16" />
    <path d="M16 16h5v5" />
  </>,
  'chevron-right': <polyline points="9 18 15 12 9 6" />,
  'menu': <>
    <line x1="4" x2="20" y1="6" y2="6" />
    <line x1="4" x2="20" y1="12" y2="12" />
    <line x1="4" x2="20" y1="18" y2="18" />
  </>,
  'x': <>
    <line x1="18" x2="6" y1="6" y2="18" />
    <line x1="6" x2="18" y1="6" y2="18" />
  </>,
  'book-open': <>
    <path d="M2 4h6a4 4 0 0 1 4 4v13a3 3 0 0 0-3-3H2Z" />
    <path d="M22 4h-6a4 4 0 0 0-4 4v13a3 3 0 0 1 3-3h7Z" />
  </>,
  'sliders': <>
    <line x1="4" x2="20" y1="6" y2="6" />
    <line x1="4" x2="20" y1="12" y2="12" />
    <line x1="4" x2="20" y1="18" y2="18" />
    <circle cx="9" cy="6" r="2" />
    <circle cx="15" cy="12" r="2" />
    <circle cx="10" cy="18" r="2" />
  </>,
  'download': <>
    <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
    <polyline points="7 10 12 15 17 10" />
    <line x1="12" x2="12" y1="15" y2="3" />
  </>,
  'home': <>
    <path d="m3 10 9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" />
    <polyline points="9 22 9 12 15 12 15 22" />
  </>,
  'radio': <>
    <circle cx="12" cy="12" r="2" />
    <path d="M16.24 7.76a6 6 0 0 1 0 8.49" />
    <path d="M7.76 16.24a6 6 0 0 1 0-8.49" />
    <path d="M19.07 4.93a10 10 0 0 1 0 14.14" />
    <path d="M4.93 19.07a10 10 0 0 1 0-14.14" />
  </>,
  'sparkles': <>
    <path d="m12 3-1.9 5.8a2 2 0 0 1-1.3 1.3L3 12l5.8 1.9a2 2 0 0 1 1.3 1.3L12 21l1.9-5.8a2 2 0 0 1 1.3-1.3L21 12l-5.8-1.9a2 2 0 0 1-1.3-1.3Z" />
  </>,
  'lock': <>
    <rect x="4" y="11" width="16" height="10" rx="2" />
    <path d="M8 11V7a4 4 0 1 1 8 0v4" />
  </>,
}

export function Icon({ name, size = 'sm', className = '' }: IconProps) {
  const body = paths[name]
  if (!body) return null
  return (
    <svg
      className={`icon icon-${size}${className ? ' ' + className : ''}`}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {body}
    </svg>
  )
}

export function BeaconMark({ className = '' }: { className?: string }) {
  return (
    <svg className={`beacon-mark${className ? ' ' + className : ''}`} viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <circle cx="12" cy="12" r="9.5" stroke="currentColor" strokeWidth="1.6" opacity="0.28" />
      <circle cx="12" cy="12" r="5.5" stroke="currentColor" strokeWidth="1.75" opacity="0.72" />
      <circle className="beacon-core" cx="12" cy="12" r="2.6" fill="currentColor" />
    </svg>
  )
}
