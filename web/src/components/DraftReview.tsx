import { useState } from 'react'
import { useAPI } from '../hooks/useAPI'

export interface Draft {
  id: string
  pipeline: string
  created_at: string
  stage_id: string
  endpoint_env?: string
  inputs: Record<string, string>
  all_outputs: Record<string, string>
  filename: string
  review_criteria?: string[]    // NEW: Sprint 15 15d
  review_reasoning?: string     // NEW: Sprint 15 15d
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
  const { approveDraft, rejectDraft } = useAPI()

  if (!draft) return null

  const handleApprove = async () => {
    setActionStatus('loading')
    setErrorMessage('')
    try {
      await approveDraft(draft.id)
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
      await rejectDraft(draft.id, rejectReason)
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

          {/* Sprint 15 15d-p2: Review criteria visualization */}
          {draft.review_criteria && draft.review_criteria.length > 0 && (
            <section className="draft-review-section">
              <h3 className="draft-review-section-title">Review criteria</h3>
              <ul className="draft-review-criteria">
                {draft.review_criteria.map((criterion, i) => (
                  <li key={i} className="draft-review-criterion">
                    <span className="criterion-icon">✅</span>
                    <span className="criterion-text">{criterion}</span>
                  </li>
                ))}
              </ul>
            </section>
          )}

          {/* Sprint 15 15d-p2: LLM reasoning */}
          {draft.review_reasoning && draft.review_reasoning.trim() !== '' && (
            <section className="draft-review-section">
              <h3 className="draft-review-section-title">Reviewer reasoning</h3>
              <div className="draft-review-reasoning">
                <p>{draft.review_reasoning}</p>
              </div>
            </section>
          )}

          {/* Sprint 15 15d-p2: Diff view (if changes were made) */}
          {Object.keys(draft.inputs).length > 0 && Object.keys(draft.all_outputs).length > 0 && (
            <section className="draft-review-section">
              <h3 className="draft-review-section-title">Changes made</h3>
              <div className="draft-review-diff">
                <div className="diff-column diff-column--before">
                  <h4>Input</h4>
                  <pre className="diff-content">
                    {Object.entries(draft.inputs).map(([k, v]) => (
                      <div key={k}><strong>{k}:</strong> {v}</div>
                    ))}
                  </pre>
                </div>
                <div className="diff-column diff-column--after">
                  <h4>Output</h4>
                  <pre className="diff-content">
                    {Object.entries(draft.all_outputs).map(([k, v]) => (
                      <div key={k}><strong>{k}:</strong> {v}</div>
                    ))}
                  </pre>
                </div>
              </div>
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