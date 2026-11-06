import { useEffect, useState } from 'react'
import type { Draft } from './DraftReview'
import { useWailsAPI } from '../hooks/useWailsAPI'

interface DraftListProps {
  onOpenDraft: (draft: Draft) => void
  refreshSignal?: number
}

export default function DraftList({ onOpenDraft, refreshSignal }: DraftListProps) {
  const [drafts, setDrafts] = useState<Draft[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const { getDrafts } = useWailsAPI()

  useEffect(() => {
    setLoading(true)
    setError(null)
    getDrafts()
      .then((data: Draft[]) => {
        setDrafts(data)
        setLoading(false)
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : 'Failed to load drafts')
        setLoading(false)
      })
  }, [refreshSignal, getDrafts])

  if (loading && drafts.length === 0) {
    return (
      <div className="draft-list draft-list--empty">
        <span className="draft-list-loading">Loading drafts...</span>
      </div>
    )
  }

  if (error) {
    return (
      <div className="draft-list draft-list--error">
        <span className="draft-list-error">Failed to load drafts: {error}</span>
      </div>
    )
  }

  if (drafts.length === 0) {
    return (
      <div className="draft-list draft-list--empty">
        <span className="draft-list-empty">No drafts pending review</span>
      </div>
    )
  }

  return (
    <div className="draft-list">
      <h3 className="draft-list-title">
        Drafts pending review
        <span className="draft-list-count">{drafts.length}</span>
      </h3>
      <ul className="draft-list-items">
        {drafts.map((draft) => (
          <li key={draft.id} className="draft-list-item">
            <button
              type="button"
              className="draft-list-item-button"
              onClick={() => onOpenDraft(draft)}
            >
              <div className="draft-list-item-main">
                <strong>{draft.pipeline}</strong>
                <span className="draft-list-item-stage">{draft.stage_id}</span>
              </div>
              <time className="draft-list-item-time">
                {formatDraftDate(draft.created_at)}
              </time>
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

function formatDraftDate(iso: string): string {
  try {
    const d = new Date(iso)
    const now = new Date()
    const diffMs = now.getTime() - d.getTime()
    const diffMin = Math.floor(diffMs / 60000)
    if (diffMin < 1) return 'just now'
    if (diffMin < 60) return `${diffMin}m ago`
    const diffH = Math.floor(diffMin / 60)
    if (diffH < 24) return `${diffH}h ago`
    const diffD = Math.floor(diffH / 24)
    return `${diffD}d ago`
  } catch {
    return iso
  }
}