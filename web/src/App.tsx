import { useEffect, useState } from 'react'
import './App.css'

// Type definitions matching the Go API response from listPipelines.
interface PipelineSummary {
  name: string
  description: string
  scheduled: boolean
  cron?: string
  stages_count: number
  locked: boolean
  paused: boolean
}

type LoadStatus = 'loading' | 'ok' | 'error'

function App() {
  const [pipelines, setPipelines] = useState<PipelineSummary[]>([])
  const [status, setStatus] = useState<LoadStatus>('loading')
  const [error, setError] = useState<string | null>(null)
  const [version, setVersion] = useState<string>('')

  useEffect(() => {
    // Fetch version for header badge.
    fetch('/version')
      .then((r) => r.json())
      .then((data) => setVersion(data.version ?? ''))
      .catch(() => setVersion('unknown'))

    // Fetch pipeline list.
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

  return (
    <div className="app">
      <header className="app-header">
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
            <article key={p.name} className="pipeline-card">
              <h2 className="pipeline-name">{p.name}</h2>
              {p.description && (
                <p className="pipeline-description">{p.description}</p>
              )}
              <div className="pipeline-meta">
                <span className="meta-item">
                  <strong>{p.stages_count}</strong> stages
                </span>
                {p.scheduled && p.cron && (
                  <span className="meta-item">
                    Cron: <code>{p.cron}</code>
                  </span>
                )}
              </div>
              <div className="pipeline-flags">
                {p.scheduled && (
                  <span className="flag flag--scheduled">Scheduled</span>
                )}
                {p.locked && <span className="flag flag--locked">Locked</span>}
                {p.paused && <span className="flag flag--paused">Paused</span>}
              </div>
            </article>
          ))}
        </div>
      )}
    </div>
  )
}

export default App