import { useCallback, useEffect, useState } from 'react'
import './App.css'
import PipelineCard, { type PipelineSummary } from './components/PipelineCard'
import MemoryViewer from './components/MemoryViewer'
import { useWebSocket, type WSEvent } from './hooks/useWebSocket'
import DraftList from './components/DraftList'
import DraftReview, { type Draft } from './components/DraftReview'

type LoadStatus = 'loading' | 'ok' | 'error'

function App() {
  const [pipelines, setPipelines] = useState<PipelineSummary[]>([])
  const [status, setStatus] = useState<LoadStatus>('loading')
  const [error, setError] = useState<string | null>(null)
  const [version, setVersion] = useState<string>('')
  const [viewerPipeline, setViewerPipeline] = useState<string | null>(null)
  const [liveEvents, setLiveEvents] = useState<WSEvent[]>([])
  const [draftRefresh, setDraftRefresh] = useState(0)
  const [reviewDraft, setReviewDraft] = useState<Draft | null>(null)

  // fetchPipelines must come BEFORE handleWsMessage (which uses it).
  const fetchPipelines = useCallback(() => {
    fetch('/api/v1/pipelines')
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((data: PipelineSummary[]) => {
        setPipelines(data)
        setStatus('ok')
      })
      .catch((err) => {
        setError(err.message)
        setStatus('error')
      })
  }, [])

  const handleWsMessage = useCallback((event: WSEvent) => {
    setLiveEvents((prev) => [event, ...prev].slice(0, 100))
    if (
      event.type === 'pipeline_completed' ||
      event.type === 'pipeline_failed' ||
      event.type === 'pipeline_paused'
    ) {
      fetchPipelines()
    }
    // Refresh draft list on completion (may create new draft)
    // or approval/rejection events (future increment).
    if (event.type === 'pipeline_completed' || event.type.startsWith('draft_')) {
      setDraftRefresh((n) => n + 1)
    }
  }, [fetchPipelines])

  const ws = useWebSocket({
    url: `ws://${window.location.host}/api/v1/ws`,
    onMessage: handleWsMessage,
  })

  useEffect(() => {
    fetch('/version')
      .then((r) => r.json())
      .then((data) => setVersion(data.version ?? ''))
      .catch(() => setVersion('unknown'))

    fetchPipelines()
  }, [fetchPipelines])

  return (
    <div className="app">
      <header className="app-header">
        <div className="app-header-main">
          <div>
            <h1 className="app-title">AI Loop</h1>
            <p className="app-subtitle">Autonomous AI Pipeline Orchestrator</p>
          </div>
          <span className={`status-badge status-badge--${status}`}>
            <span className="status-indicator" />
            {status === 'loading' && 'Loading...'}
            {status === 'ok' && `Connected${version ? ` · v${version}` : ''}`}
            {status === 'error' && 'Disconnected'}
          </span>
        </div>
        <DraftList
          onOpenDraft={setReviewDraft}
          refreshSignal={draftRefresh}
        />
        <DraftReview
          draft={reviewDraft}
          onClose={() => setReviewDraft(null)}
          onActionComplete={() => setDraftRefresh((n) => n + 1)}
        />
      </header>

      {status === 'error' && error && (
        <div className="error-message">
          Failed to load pipelines: {error}. Make sure the Go backend is running
          on <code>http://127.0.0.1:8080</code>.
        </div>
      )}

      {status === 'ok' && pipelines.length === 0 && (
        <div className="empty-state">
          <h2 className="empty-state-title">No pipelines found</h2>
          <p>
            Add a <code>.yaml</code> file to <code>config/pipelines/</code> to
            get started.
          </p>
        </div>
      )}

      {pipelines.length > 0 && (
        <div className="pipelines-grid">
          {pipelines.map((p) => (
            <PipelineCard
              key={p.name}
              pipeline={p}
              onUpdate={fetchPipelines}
              onViewMemory={setViewerPipeline}
            />
          ))}
        </div>
      )}

      <MemoryViewer
        pipelineName={viewerPipeline}
        wsStatus={ws.status}
        liveEvents={liveEvents}
        onClose={() => setViewerPipeline(null)}
      />
    </div>
  )
}

export default App