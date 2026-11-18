import { useState } from 'react'
import { type WSEvent } from '../hooks/useWebSocket'
import './ActivityFeed.css'

interface ActivityFeedProps {
  events: WSEvent[]
}

export default function ActivityFeed({ events }: ActivityFeedProps) {
  const [isCollapsed, setIsCollapsed] = useState(false)

  const formatTime = (timestamp: string) => {
    try {
      const date = new Date(timestamp)
      return date.toLocaleTimeString('pt-BR', { 
        hour: '2-digit', 
        minute: '2-digit',
        second: '2-digit'
      })
    } catch {
      return timestamp
    }
  }

  const getEventIcon = (type: string) => {
    if (type.includes('completed') || type.includes('approved')) return '✓'
    if (type.includes('failed') || type.includes('error') || type.includes('rejected')) return '✗'
    if (type.includes('started') || type.includes('created')) return '▶'
    if (type.includes('paused')) return '⏸'
    return '•'
  }

  const getEventClass = (type: string) => {
    if (type.includes('completed') || type.includes('approved')) return 'success'
    if (type.includes('failed') || type.includes('error') || type.includes('rejected')) return 'error'
    if (type.includes('started') || type.includes('created')) return 'info'
    if (type.includes('paused')) return 'warning'
    return 'info'
  }

  const getEventSummary = (event: WSEvent) => {
    const pipeline = event.payload?.pipeline || 'unknown'
    const stage = event.payload?.stage || ''
    
    if (event.type === 'pipeline_completed') {
      return `${pipeline} completed`
    }
    if (event.type === 'pipeline_failed') {
      const error = event.payload?.error || 'unknown error'
      return `${pipeline} failed: ${error.substring(0, 50)}${error.length > 50 ? '...' : ''}`
    }
    if (event.type === 'pipeline_paused') {
      const failures = event.payload?.failures || 0
      return `${pipeline} paused (${failures} failures)`
    }
    if (event.type === 'stage_completed') {
      return `${pipeline} → ${stage} completed`
    }
    if (event.type === 'stage_failed') {
      return `${pipeline} → ${stage} failed`
    }
    if (event.type === 'draft_created') {
      return `${pipeline} draft created`
    }
    if (event.type === 'draft_approved') {
      return `${pipeline} draft approved`
    }
    if (event.type === 'draft_rejected') {
      return `${pipeline} draft rejected`
    }
    if (event.type === 'cache_hit') {
      return `${pipeline} cache hit`
    }
    if (event.type === 'http_call_cost') {
      const cost = event.payload?.cost_usd
      const costValue = typeof cost === 'number' ? cost : Number(cost) || 0
      return `${pipeline} cost: $${costValue.toFixed(4)}`
    }
    
    return `${pipeline} ${event.type}`
  }

  return (
    <div className={`activity-feed ${isCollapsed ? 'activity-feed--collapsed' : ''}`}>
      <button
        type="button"
        className="activity-feed-header"
        onClick={() => setIsCollapsed(!isCollapsed)}
      >
        <div className="activity-feed-title">
          <span className="activity-feed-icon">📊</span>
          <span>Activity Feed</span>
          {events.length > 0 && (
            <span className="activity-feed-count">{events.length}</span>
          )}
        </div>
        <span className="activity-feed-toggle">
          {isCollapsed ? '▼' : '▲'}
        </span>
      </button>

      {!isCollapsed && (
        <div className="activity-feed-body">
          {events.length === 0 ? (
            <div className="activity-feed-empty">
              No activity yet. Run a pipeline to see events here.
            </div>
          ) : (
            <ul className="activity-feed-list">
              {events.slice(0, 20).map((event, index) => (
                <li
                  key={`${event.timestamp}-${index}`}
                  className={`activity-feed-item activity-feed-item--${getEventClass(event.type)}`}
                >
                  <div className="activity-feed-item-icon">
                    {getEventIcon(event.type)}
                  </div>
                  <div className="activity-feed-item-content">
                    <div className="activity-feed-item-summary">
                      {getEventSummary(event)}
                    </div>
                    <div className="activity-feed-item-time">
                      {formatTime(event.timestamp)}
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}