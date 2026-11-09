import { useCallback, useEffect, useState } from 'react'
import { useAPI } from '../hooks/useAPI'
import type { ScheduledPipeline } from '../types/pipeline'
import './CronJobManager.css'

type LoadStatus = 'idle' | 'loading' | 'ok' | 'error'

// formatRelativeTime returns a human-readable string like "in 2h 15min"
// or "in 3 days". Falls back to "—" for invalid/zero dates.
function formatRelativeTime(iso: string): string {
  if (!iso) return '—'
  const target = new Date(iso).getTime()
  if (isNaN(target)) return '—'
  const diff = target - Date.now()
  if (diff <= 0) return 'now'

  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'in less than a minute'
  if (mins < 60) return `in ${mins} min`

  const hours = Math.floor(mins / 60)
  const remMins = mins % 60
  if (hours < 24) {
    return remMins === 0 ? `in ${hours}h` : `in ${hours}h ${remMins}min`
  }

  const days = Math.floor(hours / 24)
  return `in ${days} day${days !== 1 ? 's' : ''}`
}

export default function CronJobManager() {
  const [status, setStatus] = useState<LoadStatus>('idle')
  const [timezone, setTimezone] = useState<string>('')
  const [pipelines, setPipelines] = useState<ScheduledPipeline[]>([])
  const [error, setError] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<boolean>(true)
  const [toggling, setToggling] = useState<Record<string, boolean>>({})

  const { getScheduleStatus, updatePipelineSchedule } = useAPI()

  const refresh = useCallback(() => {
    setStatus('loading')
    setError(null)
    getScheduleStatus()
      .then((data) => {
        setTimezone(data.timezone ?? '')
        setPipelines(data.pipelines ?? [])
        setStatus('ok')
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : 'Failed to load schedule')
        setStatus('error')
      })
  }, [getScheduleStatus])

  useEffect(() => {
    refresh()
  }, [refresh])

  const handleToggle = useCallback(async (pipeline: ScheduledPipeline) => {
    const newEnabled = !pipeline.enabled
    setToggling((prev) => ({ ...prev, [pipeline.name]: true }))
    try {
      await updatePipelineSchedule(pipeline.name, newEnabled)
      setPipelines((prev) =>
        prev.map((p) => (p.name === pipeline.name ? { ...p, enabled: newEnabled } : p))
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to toggle schedule')
    } finally {
      setToggling((prev) => ({ ...prev, [pipeline.name]: false }))
    }
  }, [updatePipelineSchedule])

  // Hide the entire component only when there are no scheduled pipelines
  // AND the component is expanded (user has seen the empty state).
  // When collapsed, always show the header so user can re-expand.
  if (status === 'ok' && pipelines.length === 0 && expanded) {
    // Show empty state, but allow collapse
  } else if (status === 'ok' && pipelines.length === 0 && !expanded) {
    // Show minimal header when collapsed with no pipelines
    return (
      <section className="cron-manager" aria-label="Scheduled pipelines">
        <header className="cron-manager-header">
          <button
            type="button"
            className="cron-manager-toggle"
            onClick={() => setExpanded(true)}
            aria-expanded={false}
          >
            <span className="cron-manager-title">
              📅 Scheduled pipelines
              <span className="cron-manager-count">0</span>
            </span>
            <span className="cron-manager-chevron" aria-hidden="true">▸</span>
          </button>
        </header>
      </section>
    )
  }

  return (
    <section className="cron-manager" aria-label="Scheduled pipelines">
      <header className="cron-manager-header">
        <button
          type="button"
          className="cron-manager-toggle"
          onClick={() => setExpanded((v) => !v)}
          aria-expanded={expanded}
        >
          <span className="cron-manager-title">
            📅 Scheduled pipelines
            {status === 'ok' && (
              <span className="cron-manager-count">{pipelines.length}</span>
            )}
          </span>
          <span className="cron-manager-chevron" aria-hidden="true">
            {expanded ? '▾' : '▸'}
          </span>
        </button>
        {timezone && expanded && (
          <span className="cron-manager-timezone">
            All times in: <code>{timezone}</code>
          </span>
        )}
        {expanded && (
          <button
            type="button"
            className="btn btn--ghost btn--small"
            onClick={refresh}
            disabled={status === 'loading'}
          >
            {status === 'loading' ? 'Refreshing...' : 'Refresh'}
          </button>
        )}
      </header>

      {expanded && (
        <div className="cron-manager-body">
          {status === 'loading' && pipelines.length === 0 && (
            <p className="cron-manager-status">Loading schedule...</p>
          )}

          {status === 'error' && error && (
            <div className="cron-manager-error" role="alert">
              {error}
            </div>
          )}

          {status === 'ok' && pipelines.length === 0 && (
            <p className="cron-manager-empty">
              No pipelines are currently scheduled. Enable a schedule in the
              Pipeline Editor to see them here.
            </p>
          )}

          {status === 'ok' && pipelines.length > 0 && (
            <ul className="cron-manager-list">
              {pipelines.map((p) => (
                <li key={p.name} className="cron-manager-item">
                  <div className="cron-manager-item-main">
                    <div className="cron-manager-name">
                      {p.name}
                      {p.locked && (
                        <span className="cron-manager-flag cron-manager-flag--locked">
                          Running
                        </span>
                      )}
                      {p.paused && (
                        <span className="cron-manager-flag cron-manager-flag--paused">
                          Paused
                        </span>
                      )}
                    </div>
                    <div className="cron-manager-meta">
                      <code className="cron-manager-cron">{p.cron}</code>
                      <span
                        className="cron-manager-next"
                        title={new Date(p.next_execution).toLocaleString()}
                      >
                        Next: <strong>{formatRelativeTime(p.next_execution)}</strong>
                      </span>
                    </div>
                  </div>

                  <label className="cron-manager-switch" title={p.enabled ? 'Disable schedule' : 'Enable schedule'}>
                    <input
                      type="checkbox"
                      checked={p.enabled}
                      disabled={Boolean(toggling[p.name])}
                      onChange={() => handleToggle(p)}
                      aria-label={`${p.enabled ? 'Disable' : 'Enable'} schedule for ${p.name}`}
                    />
                    <span className="cron-manager-switch-slider" aria-hidden="true" />
                  </label>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </section>
  )
}