import { useEffect, useState, useCallback, useMemo } from 'react'
import type { WSEvent } from '../hooks/useWebSocket'

// Shape of events returned by GET /api/v1/pipelines/{name}/memory
interface MemoryEvent {
  id: string
  pipeline_id: string
  stage_id?: string
  timestamp: number
  type: string
  data?: Record<string, unknown>
}

interface MemoryViewerProps {
  pipelineName: string | null // null = closed
  wsStatus: 'connecting' | 'open' | 'closed' | 'error'
  liveEvents: WSEvent[] // all live events from WS, filtered inside
  onClose: () => void
}

export default function MemoryViewer({
  pipelineName,
  wsStatus,
  liveEvents,
  onClose,
}: MemoryViewerProps) {
  const [history, setHistory] = useState<MemoryEvent[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const fetchHistory = useCallback(async () => {
    if (!pipelineName) return
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(
        `/api/v1/pipelines/${encodeURIComponent(pipelineName)}/memory?limit=50`
      )
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const body = await res.json()
      setHistory(Array.isArray(body.events) ? body.events : [])
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load memory')
    } finally {
      setLoading(false)
    }
  }, [pipelineName])

  // Fetch history when viewer opens or pipeline changes.
  useEffect(() => {
    if (pipelineName) {
      fetchHistory()
    } else {
      setHistory([])
      setError(null)
    }
  }, [pipelineName, fetchHistory])

  // Close on ESC key.
  useEffect(() => {
    if (!pipelineName) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [pipelineName, onClose])

  // Filter live events to only those for this pipeline.
  const relevantLive = useMemo(
    () =>
      liveEvents.filter((e) => {
        const p = e.payload?.pipeline
        return typeof p === 'string' && p === pipelineName
      }),
    [liveEvents, pipelineName]
  )

  if (!pipelineName) return null

  return (
    <>
      <div className="memory-backdrop" onClick={onClose} />
      <aside className="memory-drawer" role="dialog" aria-label={`Memory: ${pipelineName}`}>
        <header className="memory-header">
          <div>
            <h2 className="memory-title">Memory · {pipelineName}</h2>
            <span className={`memory-ws memory-ws--${wsStatus}`}>
              <span className="memory-ws-dot" />
              {wsStatus === 'open' && 'Live'}
              {wsStatus === 'connecting' && 'Connecting…'}
              {wsStatus === 'closed' && 'Disconnected'}
              {wsStatus === 'error' && 'Error'}
            </span>
          </div>
          <div className="memory-header-actions">
            <button
              type="button"
              className="btn btn--ghost"
              onClick={fetchHistory}
              disabled={loading}
            >
              {loading ? 'Refreshing…' : 'Refresh'}
            </button>
            <button
              type="button"
              className="btn btn--ghost"
              onClick={onClose}
              aria-label="Close"
            >
              ✕
            </button>
          </div>
        </header>

        <div className="memory-body">
          {error && (
            <div className="memory-error" role="alert">
              {error}
            </div>
          )}

          {/* Live events section */}
          <section className="memory-section">
            <h3 className="memory-section-title">
              Live events{' '}
              <span className="memory-count">{relevantLive.length}</span>
            </h3>
            {relevantLive.length === 0 ? (
              <p className="memory-empty">
                No live events yet. Run the pipeline to see real-time updates.
              </p>
            ) : (
              <ul className="memory-list">
                {relevantLive.map((ev, idx) => (
                  <li
                    key={`${ev.timestamp}-${idx}`}
                    className={`memory-item memory-item--${classifyLiveType(ev.type)}`}
                  >
                    <div className="memory-item-header">
                      <span className="memory-type">{ev.type}</span>
                      <time className="memory-time">
                        {formatTimestamp(ev.timestamp)}
                      </time>
                    </div>
                    {ev.payload && (
                      <pre className="memory-payload">
                        {JSON.stringify(ev.payload, null, 2)}
                      </pre>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>

          {/* Persisted history section */}
          <section className="memory-section">
            <h3 className="memory-section-title">
              History <span className="memory-count">{history.length}</span>
            </h3>
            {loading && history.length === 0 ? (
              <p className="memory-empty">Loading…</p>
            ) : history.length === 0 ? (
              <p className="memory-empty">No persisted events yet.</p>
            ) : (
              <ul className="memory-list">
                {history.map((ev) => (
                  <li
                    key={ev.id}
                    className={`memory-item memory-item--${classifyHistoryType(ev.type)}`}
                  >
                    <div className="memory-item-header">
                      <span className="memory-type">{ev.type}</span>
                      {ev.stage_id && (
                        <span className="memory-stage">{ev.stage_id}</span>
                      )}
                      <time className="memory-time">
                        {formatUnix(ev.timestamp)}
                      </time>
                    </div>
                    {ev.data && Object.keys(ev.data).length > 0 && (
                      <pre className="memory-payload">
                        {JSON.stringify(ev.data, null, 2)}
                      </pre>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      </aside>
    </>
  )
}

// --- Helpers ------------------------------------------------------------

function classifyLiveType(type: string): string {
  if (type.includes('completed') || type.includes('success')) return 'success'
  if (type.includes('failed') || type.includes('error')) return 'error'
  if (type.includes('paused')) return 'warning'
  return 'info'
}

function classifyHistoryType(type: string): string {
  if (type.includes('complete') || type.includes('success')) return 'success'
  if (type.includes('failed') || type.includes('error')) return 'error'
  return 'info'
}

function formatTimestamp(iso: string): string {
  try {
    const d = new Date(iso)
    return d.toLocaleTimeString('en-US', { hour12: false })
  } catch {
    return iso
  }
}

function formatUnix(seconds: number): string {
  try {
    const d = new Date(seconds * 1000)
    return d.toLocaleTimeString('en-US', { hour12: false })
  } catch {
    return String(seconds)
  }
}