import { useCallback, useEffect, useState } from 'react'
import './App.css'
import PipelineCard, { type PipelineSummary } from './components/PipelineCard'
import MemoryViewer from './components/MemoryViewer'
import { useWebSocket, getWsUrl, type WSEvent } from './hooks/useWebSocket'
import DraftList from './components/DraftList'
import DraftReview, { type Draft } from './components/DraftReview'
import { ToastContainer, type ToastData } from './components/Toast'
import { useAPI } from './hooks/useAPI'
import BudgetCard from './components/BudgetCard'
import PipelineEditor from './components/PipelineEditor'
import type { Pipeline } from './types/pipeline'

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
  const [toasts, setToasts] = useState<ToastData[]>([])
  const { listPipelines } = useAPI()
  const [editorMode, setEditorMode] = useState<'closed' | 'create' | 'edit'>('closed')
  const [editingPipeline, setEditingPipeline] = useState<Pipeline | null>(null)

  const addToast = useCallback((type: ToastData['type'], message: string) => {
    const id = Date.now() + Math.random()
    setToasts((prev) => [...prev.slice(-4), { id, type, message }])
  }, [])

  const dismissToast = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id))
  }, [])

  const fetchPipelines = useCallback(() => {
    listPipelines()
      .then((data: PipelineSummary[]) => {
        setPipelines(data)
        setStatus('ok')
      })
      .catch((err) => {
        setError(err.message)
        setStatus('error')
      })
  }, [listPipelines])

  const handleCreatePipeline = useCallback(() => {
    setEditorMode('create')
    setEditingPipeline(null)
  }, [])

  const handleEditPipeline = useCallback(async (name: string) => {
    try {
      const detail = await fetch(`/api/v1/pipelines/${encodeURIComponent(name)}`).then(r => r.json())
      setEditingPipeline(detail as Pipeline)
      setEditorMode('edit')
    } catch (err) {
      addToast('error', `Failed to load pipeline: ${err instanceof Error ? err.message : 'unknown'}`)
    }
  }, [addToast])

  const handleEditorClose = useCallback(() => {
    setEditorMode('closed')
    setEditingPipeline(null)
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

    // Toast notifications for draft lifecycle events (G3.2 UX polish)
    switch (event.type) {
      case 'draft_created':
        addToast('info', `New draft created: ${event.payload?.pipeline ?? 'unknown'}`)
        break
      case 'draft_approved':
        addToast('success', `Draft approved: ${event.payload?.pipeline ?? 'unknown'}`)
        break
      case 'draft_rejected':
        addToast('error', `Draft rejected: ${event.payload?.pipeline ?? 'unknown'}`)
        break
    }
  }, [fetchPipelines])

  const ws = useWebSocket({
    url: getWsUrl(),
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
          <ToastContainer toasts={toasts} onDismiss={dismissToast} />
        </div>
        <BudgetCard />
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

            {status === 'ok' && (
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
          <div>
            {pipelines.length === 0 && (
              <p style={{ color: '#94a3b8' }}>No pipelines yet. Create your first one to get started.</p>
            )}
            {pipelines.length > 0 && (
              <p style={{ color: '#94a3b8' }}>{pipelines.length} pipeline{pipelines.length !== 1 ? 's' : ''}</p>
            )}
          </div>
          <button
            type="button"
            className="btn btn--primary"
            onClick={handleCreatePipeline}
          >
            + Create pipeline
          </button>
        </div>
      )}

      {status === 'ok' && (
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
          <div>
            {pipelines.length === 0 && (
              <p style={{ color: '#94a3b8' }}>No pipelines yet. Create your first one to get started.</p>
            )}
            {pipelines.length > 0 && (
              <p style={{ color: '#94a3b8' }}>{pipelines.length} pipeline{pipelines.length !== 1 ? 's' : ''}</p>
            )}
          </div>
          <button
            type="button"
            className="btn btn--primary"
            onClick={handleCreatePipeline}
          >
            + Create pipeline
          </button>
        </div>
      )}

      {status === 'ok' && pipelines.length === 0 && (
        <div className="empty-state">
          <h2 className="empty-state-title">No pipelines found</h2>
          <p>
            Click "Create pipeline" above to build your first autonomous AI workflow visually.
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
              onEdit={handleEditPipeline}
            />
          ))}
        </div>
      )}

      {editorMode !== 'closed' && (
        <PipelineEditor
          pipeline={editingPipeline}
          onClose={handleEditorClose}
          onSaved={fetchPipelines}
        />
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