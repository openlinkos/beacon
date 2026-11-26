import { useSyncExternalStore } from 'react'
import type { ComponentProps } from 'react'

export const base = '/workspace/'
const pushEvent = 'beacon-navigation'

function subscribe(callback: () => void) {
  addEventListener('popstate', callback)
  addEventListener('hashchange', callback)
  addEventListener(pushEvent, callback)
  return () => {
    removeEventListener('popstate', callback)
    removeEventListener('hashchange', callback)
    removeEventListener(pushEvent, callback)
  }
}

export function useLocation() {
  return useSyncExternalStore(subscribe, () => location.href)
}

export function navigate(path: string) {
  history.pushState(null, '', path)
  dispatchEvent(new Event(pushEvent))
}

export function workspaceHref(href: string) {
  if (/^\/console(?:[?#]|$)/.test(href)) return base + href.slice('/console'.length)
  if (/^\/console\/alerts(?:\?|#|$)/.test(href) ||
      /^\/console\/alerts\/[^/?#]+(?:\/episodes\/[^/?#]+|\/share-links)?(?:\?|#|$)/.test(href)) {
    return base + href.slice('/console/'.length)
  }
  if (/^\/console\/(?:sign-in|setup|join|reset|account|people|agents|capabilities|machines|monitors|sessions|maintenance|silences|status-pages|credentials|tokens|config|hooks|audit|changes|incident-outcomes|incident-episodes)(?:[/?#]|$)/.test(href) &&
      !/^\/console\/sessions\/[^/]+\/recording(?:[?#]|$)/.test(href)) return base + href.slice('/console/'.length)
  return href
}

export function Link({ href = '', onClick, ...props }: ComponentProps<'a'>) {
  const to = workspaceHref(href)
  return <a {...props} href={to} onClick={event => {
    onClick?.(event)
    if (event.defaultPrevented || !to.startsWith(base) || event.button !== 0 ||
        props.target || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
    event.preventDefault()
    const current = new URL(location.href)
    const target = new URL(to, current)
    if (current.pathname !== target.pathname || current.search !== target.search) window.scrollTo(0, 0)
    navigate(to)
  }} />
}
