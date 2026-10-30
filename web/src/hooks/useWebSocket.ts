import { useEffect, useRef, useState, useCallback } from 'react'

// Event types emitted by the Go backend via scheduler broadcasts.
// See internal/api/ws.go → WSEvent
export interface WSEvent {
  type: string
  timestamp: string
  payload?: {
    pipeline?: string
    status?: string
    error?: string
    failures?: number
    outputs?: Record<string, string>
    [key: string]: unknown
  }
}

interface UseWebSocketOptions {
  url: string
  onMessage?: (event: WSEvent) => void
  reconnectDelayMs?: number
}

interface UseWebSocketReturn {
  status: 'connecting' | 'open' | 'closed' | 'error'
  lastEvent: WSEvent | null
  reconnect: () => void
}

/**
 * useWebSocket maintains a single persistent WebSocket connection to the
 * backend hub and dispatches incoming events via the onMessage callback.
 *
 * Reconnects automatically on close/error with the configured delay.
 * The connection is closed when the consuming component unmounts.
 */
export function useWebSocket({
  url,
  onMessage,
  reconnectDelayMs = 2000,
}: UseWebSocketOptions): UseWebSocketReturn {
  const [status, setStatus] = useState<UseWebSocketReturn['status']>('connecting')
  const [lastEvent, setLastEvent] = useState<WSEvent | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectTimer = useRef<number | null>(null)
  const unmountedRef = useRef(false)

  const connect = useCallback(() => {
    if (unmountedRef.current) return

    setStatus('connecting')
    const ws = new WebSocket(url)
    wsRef.current = ws

    ws.onopen = () => {
      if (!unmountedRef.current) setStatus('open')
    }

    ws.onmessage = (ev) => {
      try {
        const parsed: WSEvent = JSON.parse(ev.data)
        setLastEvent(parsed)
        onMessage?.(parsed)
      } catch (err) {
        console.error('ws: failed to parse message', err)
      }
    }

    ws.onerror = () => {
      if (!unmountedRef.current) setStatus('error')
    }

    ws.onclose = () => {
      if (!unmountedRef.current) {
        setStatus('closed')
        // Auto-reconnect after delay.
        reconnectTimer.current = window.setTimeout(() => {
          if (!unmountedRef.current) connect()
        }, reconnectDelayMs)
      }
    }
  }, [url, onMessage, reconnectDelayMs])

  useEffect(() => {
    unmountedRef.current = false
    connect()
    return () => {
      unmountedRef.current = true
      if (reconnectTimer.current !== null) {
        window.clearTimeout(reconnectTimer.current)
      }
      if (wsRef.current) {
        wsRef.current.close()
        wsRef.current = null
      }
    }
  }, [connect])

  const reconnect = useCallback(() => {
    if (wsRef.current) wsRef.current.close()
  }, [])

  return { status, lastEvent, reconnect }
}