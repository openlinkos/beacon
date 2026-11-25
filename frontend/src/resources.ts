import { useEffect, useState } from 'react'
import { APIError, request } from './api'

type Resource<T> = { data?: T; error?: APIError; pending: boolean }

export function useResource<T>(path: string, version = 0): Resource<T> {
  const [state, setState] = useState<{ path: string; result: Resource<T> }>({
    path, result: { pending: true },
  })
  useEffect(() => {
    const controller = new AbortController()
    request<T>(path, { signal: controller.signal }).then(
      data => setState({ path, result: { data, pending: false } }),
      reason => {
        if (!controller.signal.aborted) {
          setState({ path, result: { error: reason instanceof APIError ? reason : new APIError(0, '', String(reason)), pending: false } })
        }
      },
    )
    return () => controller.abort()
  }, [path, version])
  return state.path === path ? state.result : { pending: true }
}
