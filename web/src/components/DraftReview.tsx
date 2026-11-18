import { useState } from 'react'
import { useAPI } from '../hooks/useAPI'
import TypewriterText from './TypewriterText'
import { getTypewriterPreference, setTypewriterPreference } from '../hooks/useTypewriter'
import React from 'react'

export interface Draft {
  id: string
  pipeline: string
  created_at: string
  stage_id: string
  endpoint_env?: string
  inputs: Record<string, string>
  all_outputs: Record<string, string>
  filename: string
  review_criteria?: string[]
  review_reasoning?: string
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
  const [typewriterEnabled, setTypewriterEnabled] = useState<boolean>(
    () => getTypewriterPreference()
  )
  const { approveDraft, rejectDraft } = useAPI()

  const handleApprove = async () => {
    if (!draft) return
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
    if (!draft) return
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

  const handleToggleTypewriter = () => {
    const newValue = !typewriterEnabled
    setTypewriterEnabled(newValue)
    setTypewriterPreference(newValue)
  }

  React.useEffect(() => {
    setActionStatus('idle')
    setRejectReason('')
    setErrorMessage('')
  }, [draft?.id])

  if (!draft) return null

  // Get the final output (the content that will be published)
  // For news_blog: post_aprovado is the final stage output
  const finalOutputKey = Object.keys(draft.all_outputs).pop() || ''
  const finalContent = finalOutputKey ? draft.all_outputs[finalOutputKey] : ''

  // Quality gate status: if review_reasoning exists and contains PASS, it passed
  const hasQualityGate = draft.review_criteria && draft.review_criteria.length > 0
  const qualityPassed = draft.review_reasoning?.toLowerCase().includes('"decision": "pass"') ||
                        draft.review_reasoning?.toLowerCase().includes('decision: pass') ||
                        !draft.review_reasoning // no reasoning = auto-pass (no review stage)

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

          {/* Quality Gate Card — Sprint 19d: prominent at the top */}
          {hasQualityGate && (
            <section className="quality-gate-card">
              <div className="quality-gate-header">
                <h3 className="quality-gate-title">
                  <span className="quality-gate-icon">
                    {qualityPassed ? '✓' : '⚠'}
                  </span>
                  Quality Gate
                </h3>
                <span className={`quality-gate-status quality-gate-status--${qualityPassed ? 'passed' : 'failed'}`}>
                  {qualityPassed ? 'PASSED' : 'NEEDS REVIEW'}
                </span>
              </div>

              <ul className="quality-gate-criteria">
                {draft.review_criteria?.map((criterion, i) => (
                  <li key={i} className="quality-gate-criterion">
                    <span className="criterion-check">✓</span>
                    <span className="criterion-label">{formatCriterionLabel(criterion)}</span>
                  </li>
                ))}
              </ul>

              {draft.review_reasoning && draft.review_reasoning.trim() !== '' && (
                <div className="quality-gate-reasoning">
                  <p className="quality-gate-reasoning-label">Reviewer reasoning:</p>
                  <p className="quality-gate-reasoning-text">{draft.review_reasoning}</p>
                </div>
              )}
            </section>
          )}

          {/* Generated Content — Sprint 19d: single content view */}
          <section className="content-card">
            <div className="content-card-header">
              <h3 className="content-card-title">Generated content</h3>
              <label className="typewriter-toggle" title="Typewriter animation">
                <input
                  type="checkbox"
                  checked={typewriterEnabled}
                  onChange={handleToggleTypewriter}
                />
                <span className="typewriter-toggle-label">
                  ✨ Typewriter
                </span>
              </label>
            </div>
            <div className="content-card-body">
              {finalContent ? (
                <pre className="content-card-text">
                  <TypewriterText
                    text={finalContent}
                    enabled={typewriterEnabled}
                    speed={20}
                  />
                </pre>
              ) : (
                <p className="content-card-empty">No content generated</p>
              )}
            </div>
          </section>

          {/* Rejection reason */}
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

// Convert snake_case criterion IDs to human-readable labels
function formatCriterionLabel(criterion: string): string {
  const labels: Record<string, string> = {
    'valid_yaml_frontmatter': 'Valid YAML frontmatter',
    'brazilian_portuguese_language': 'Brazilian Portuguese language',
    'correct_markdown_structure': 'Correct markdown structure',
    'editorial_coherence': 'Editorial coherence',
  }
  return labels[criterion] || criterion.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())
}