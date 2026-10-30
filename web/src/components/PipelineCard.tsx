import { useState } from 'react'

export interface PipelineSummary {
  name: string
  description: string
  scheduled: boolean
  cron?: string
  stages_count: number
  locked: boolean
  paused: boolean
}

type ActionStatus = 'idle' | 'loading' | 'success' | 'error'

interface PipelineCardProps {
  pipeline: PipelineSummary
  onUpdate: () => void
  onViewMemory: (pipelineName: string) => void
}

export default function PipelineCard({
  pipeline,
  onUpdate,
  onViewMemory,
}: PipelineCardProps) {
  const [runStatus, setRunStatus] = useState<ActionStatus>('idle')
  const [resumeStatus, setResumeStatus] = useState<ActionStatus>('idle')
  const [message, setMessage] = useState<string>('')

  const handleRun = async () => {
    if (pipeline.locked) return

    setRunStatus('loading')
    setMessage('Running...')
    try {
      const res = await fetch(`/api/v1/pipelines/${pipeline.name}/run`, {
        method: 'POST',
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
        throw new Error(body.error ?? `HTTP ${res.status}`)
      }
      const data = await res.json()
      setRunStatus('success')
      setMessage(`Completed: ${data.status}`)
      onUpdate()
    } catch (err) {
      setRunStatus('error')
      setMessage(err instanceof Error ? err.message : 'Unknown error')
    }
  }

  const handleResume = async () => {
    if (pipeline.locked) return

    setResumeStatus('loading')
    setMessage('Resuming...')
    try {
      const res = await fetch(`/api/v1/pipelines/${pipeline.name}/failures`, {
        method: 'DELETE',
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
        throw new Error(body.error ?? `HTTP ${res.status}`)
      }
      setResumeStatus('success')
      setMessage('Resumed successfully')
      onUpdate()
    } catch (err) {
      setResumeStatus('error')
      setMessage(err instanceof Error ? err.message : 'Unknown error')
    }
  }

  return (
    <article className="pipeline-card">
      <h2 className="pipeline-name">{pipeline.name}</h2>
      {pipeline.description && (
        <p className="pipeline-description">{pipeline.description}</p>
      )}

      <div className="pipeline-meta">
        <span className="meta-item">
          <strong>{pipeline.stages_count}</strong> stages
        </span>
        {pipeline.scheduled && pipeline.cron && (
          <span className="meta-item">
            Cron: <code>{pipeline.cron}</code>
          </span>
        )}
      </div>

      <div className="pipeline-flags">
        {pipeline.scheduled && (
          <span className="flag flag--scheduled">Scheduled</span>
        )}
        {pipeline.locked && <span className="flag flag--locked">Locked</span>}
        {pipeline.paused && <span className="flag flag--paused">Paused</span>}
      </div>

      <div className="pipeline-actions">
        <button
          type="button"
          className={`btn btn--primary ${runStatus === 'loading' ? 'btn--loading' : ''}`}
          onClick={handleRun}
          disabled={pipeline.locked || runStatus === 'loading' || resumeStatus === 'loading'}
          title={pipeline.locked ? 'Pipeline is currently running' : 'Trigger a new run'}
        >
          {runStatus === 'loading' ? 'Running...' : 'Run now'}
        </button>

        {pipeline.paused && (
          <button
            type="button"
            className={`btn btn--warning ${resumeStatus === 'loading' ? 'btn--loading' : ''}`}
            onClick={handleResume}
            disabled={pipeline.locked || runStatus === 'loading' || resumeStatus === 'loading'}
            title="Reset failure counter and resume"
          >
            {resumeStatus === 'loading' ? 'Resuming...' : 'Resume'}
          </button>
        )}

        <button
          type="button"
          className="btn btn--secondary"
          onClick={() => onViewMemory(pipeline.name)}
          title="View pipeline memory and live events"
        >
          View memory
        </button>
      </div>

      {message && (
        <div
          className={`pipeline-message pipeline-message--${runStatus === 'error' || resumeStatus === 'error' ? 'error' : 'info'}`}
          role="status"
        >
          {message}
        </div>
      )}
    </article>
  )
}