import { useState } from 'react'

export interface Draft {
  id: string
  pipeline: string
  created_at: string
  stage_id: string
  endpoint_env?: string
  inputs: Record<string, string>
  all_outputs: Record<string, string>
  filename: string
}

type ActionStatus = 'idle' | 'loading' | 'success' | 'error'

interface DraftReviewProps {
  draft: Draft | null
  onClose: () => void
  onActionComplete: () => void
}

export default function DraftReview({ draft, onClose, onActionComplete }: DraftReviewProps) {
  const [actionStatus, setActionStatus] = useState<ActionStatus>('idle')
  const [rejectReason, setRejectReason] = useState('')
  const [errorMessage, setErrorMessage] = useState<string>('')

  if (!draft) return null

  const handleApprove = async () => {
    setActionStatus('loading')
    setErrorMessage('')
    try {
      const res = await fetch(`/api/v1/drafts/${draft.id}/approve`, {
        method: 'POST',
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
        throw new Error(body.error ?? `HTTP ${res.status}`)
      }
      setActionStatus('success')
      setTimeout(() => {
        onActionComplete()
        onClose()
      }, 800)
    } catch (err) {
      setActionStatus('error')
      setErrorMessage(err instanceof Error ? err.message : 'Approve failed')
    }
  }

  const handleReject = async () => {
    if (!rejectReason.trim()) {
      setErrorMessage('Please provide a reason for rejection')
      return
    }
    setActionStatus('loading')
    setErrorMessage('')
    try {
      const res = await fetch(`/api/v1/drafts/${draft.id}/reject`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reason: rejectReason }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
        throw new Error(body.error ?? `HTTP ${res.status}`)
      }
      setActionStatus('success')
      setTimeout(() => {
        onActionComplete()
        onClose()
      }, 800)
    } catch (err) {
      setActionStatus('error')
      setErrorMessage(err instanceof Error ? err.message : 'Reject failed')
    }
  }

  return (
    <>
      <div className="draft-review-backdrop" onClick={onClose} />
      <div className="draft-review-modal" role="dialog" aria-label="Draft review">
        <header className="draft-review-header">
          <div>
            <h2 className="draft-review-title">Review draft</h2>
            <p className="draft-review-subtitle">
              <strong>{draft.pipeline}</strong> · {draft.stage_id} · {formatDraftDate(draft.created_at)}
            </p>
          </div>
          <button
            type="button"
            className="btn btn--ghost"
            onClick={onClose}
            aria-label="Close"
          >
            ✕
          </button>
        </header>

        <div className="draft-review-body">
          {errorMessage && (
            <div className="draft-review-error" role="alert">
              {errorMessage}
            </div>
          )}

          {actionStatus === 'success' && (
            <div className="draft-review-success" role="status">
              ✓ Action completed successfully
            </div>
          )}

          <section className="draft-review-section">
            <h3 className="draft-review-section-title">Pipeline outputs</h3>
            <div className="draft-review-outputs">
              {Object.entries(draft.all_outputs).map(([key, value]) => (
                <div key={key} className="draft-review-output">
                  <div className="draft-review-output-key">{key}</div>
                  <pre className="draft-review-output-value">{value}</pre>
                </div>
              ))}
            </div>
          </section>

          {draft.endpoint_env && (
            <section className="draft-review-section">
              <h3 className="draft-review-section-title">Publish target</h3>
              <code className="draft-review-endpoint">
                env: {draft.endpoint_env}
              </code>
            </section>
          )}

          <section className="draft-review-section">
            <h3 className="draft-review-section-title">Rejection reason (if rejecting)</h3>
            <textarea
              className="draft-review-reason"
              placeholder="Explain why this draft should not be published..."
              value={rejectReason}
              onChange={(e) => setRejectReason(e.target.value)}
              disabled={actionStatus === 'loading' || actionStatus === 'success'}
              rows={3}
            />
          </section>
        </div>

        <footer className="draft-review-footer">
          <button
            type="button"
            className="btn btn--danger"
            onClick={handleReject}
            disabled={actionStatus === 'loading' || actionStatus === 'success'}
          >
            {actionStatus === 'loading' && rejectReason ? 'Rejecting...' : 'Reject'}
          </button>
          <button
            type="button"
            className="btn btn--success"
            onClick={handleApprove}
            disabled={actionStatus === 'loading' || actionStatus === 'success'}
          >
            {actionStatus === 'loading' && !rejectReason ? 'Approving...' : 'Approve & Publish'}
          </button>
        </footer>
      </div>
    </>
  )
}

function formatDraftDate(iso: string): string {
  try {
    return new Date(iso).toLocaleString()
  } catch {
    return iso
  }
}