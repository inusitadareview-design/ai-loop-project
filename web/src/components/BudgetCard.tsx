import { useCallback, useEffect, useState } from 'react'
import { useAPI } from '../hooks/useAPI'

interface BudgetData {
  daily_limit_usd: number
  spent_usd: number
  remaining_usd: number
}

type LoadStatus = 'loading' | 'ok' | 'error'

const POLL_INTERVAL_MS = 30_000 // 30 seconds

export default function BudgetCard() {
  const [status, setStatus] = useState<LoadStatus>('loading')
  const [data, setData] = useState<BudgetData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const { getBudget } = useAPI()

  const fetchBudget = useCallback(async () => {
    try {
      const result = await getBudget()
      setData(result)
      setStatus('ok')
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load budget')
      setStatus('error')
    }
  }, [getBudget])

  // Initial load + polling every 30 seconds
  useEffect(() => {
    fetchBudget()
    const interval = setInterval(fetchBudget, POLL_INTERVAL_MS)
    return () => clearInterval(interval)
  }, [fetchBudget])

  // Calculate percentage and color class
  const getProgressInfo = (): { pct: number; colorClass: string } => {
    if (!data || data.daily_limit_usd <= 0) {
      return { pct: 0, colorClass: 'budget-bar--green' }
    }
    const pct = (data.spent_usd / data.daily_limit_usd) * 100
    const clamped = Math.min(100, Math.max(0, pct))
    
    let colorClass = 'budget-bar--green'
    if (pct >= 90) colorClass = 'budget-bar--red'
    else if (pct >= 50) colorClass = 'budget-bar--yellow'
    
    return { pct: clamped, colorClass }
  }

  const { pct, colorClass } = getProgressInfo()

  const formatUSD = (n: number | undefined): string =>
  `$${(n ?? 0).toFixed(2)}`

  return (
    <section className="budget-card" aria-label="Daily budget">
      <header className="budget-card-header">
        <h2 className="budget-card-title">Daily Budget</h2>
        <span className={`status-indicator status-indicator--${status}`} />
      </header>

      {status === 'loading' && !data && (
        <div className="budget-card-loading">Loading budget...</div>
      )}

      {status === 'error' && !data && (
        <div className="budget-card-error">
          <p>{error}</p>
          <button
            type="button"
            className="btn btn--secondary btn--small"
            onClick={fetchBudget}
          >
            Retry
          </button>
        </div>
      )}

      {data && (
        <>
          <div className="budget-card-stats">
            <div className="budget-stat">
              <span className="budget-stat-label">Daily Limit</span>
              <span className="budget-stat-value">
                {formatUSD(data.daily_limit_usd)}
              </span>
            </div>
            <div className="budget-stat">
              <span className="budget-stat-label">Spent Today</span>
              <span className="budget-stat-value budget-stat-value--spent">
                {formatUSD(data.spent_usd)}
              </span>
            </div>
            <div className="budget-stat">
              <span className="budget-stat-label">Remaining</span>
              <span className="budget-stat-value budget-stat-value--remaining">
                {formatUSD(data.remaining_usd)}
              </span>
            </div>
          </div>

          <div className="budget-bar-container" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
            <div
              className={`budget-bar ${colorClass}`}
              style={{ width: `${pct}%` }}
            />
          </div>

          <div className="budget-card-footer">
            <small>{pct.toFixed(1)}% of daily budget used</small>
          </div>
        </>
      )}
    </section>
  )
}