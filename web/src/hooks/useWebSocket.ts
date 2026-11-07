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
 * getWsUrl returns the correct WebSocket URL based on the runtime environment.
 * 
 * In Wails dev mode, window.location.host is "wails.localhost:PORT" which does
 * NOT proxy WebSocket connections to the backend. In this case, we connect
 * directly to the Go backend at 127.0.0.1:8080.
 * 
 * In browser mode (pnpm dev), Vite proxies /api/v1/ws to the backend, so we
 * use the same host as the current page.
 */
export function getWsUrl(): string {
  // Wails dev mode: connect directly to the Go backend
  if (window.location.hostname === 'wails.localhost') {
    return 'ws://127.0.0.1:8080/api/v1/ws'
  }
  
  // Browser mode: use same host (Vite proxy handles routing)
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//${window.location.host}/api/v1/ws`
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