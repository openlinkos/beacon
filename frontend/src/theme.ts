import { useSyncExternalStore } from 'react'

export type ThemeMode = 'system' | 'light' | 'dark'

const storageKey = 'beacon-theme'
const themeEvent = 'beacon-theme-change'

function readStored(): ThemeMode {
  try {
    const raw = localStorage.getItem(storageKey)
    if (raw === 'light' || raw === 'dark' || raw === 'system') return raw
  } catch {
    // Ignore storage access errors in restricted browser contexts.
  }
  return 'system'
}

export function applyTheme(mode: ThemeMode) {
  const root = document.documentElement
  if (mode === 'system') {
    delete root.dataset.theme
  } else {
    root.dataset.theme = mode
  }
}

export function setTheme(mode: ThemeMode) {
  try {
    localStorage.setItem(storageKey, mode)
  } catch {
    // Ignore storage errors.
  }
  applyTheme(mode)
  dispatchEvent(new Event(themeEvent))
}

function subscribe(callback: () => void) {
  addEventListener(themeEvent, callback)
  addEventListener('storage', callback)
  return () => {
    removeEventListener(themeEvent, callback)
    removeEventListener('storage', callback)
  }
}

export function useTheme(): [ThemeMode, (mode: ThemeMode) => void] {
  const mode = useSyncExternalStore<ThemeMode>(subscribe, readStored, () => 'system')
  return [mode, setTheme]
}

// Apply stored preference immediately at module load to avoid flash on navigation.
applyTheme(readStored())
