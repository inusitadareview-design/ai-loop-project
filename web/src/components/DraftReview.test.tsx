import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import DraftReview, { type Draft } from './DraftReview'

const mockFetch = vi.fn()
vi.stubGlobal('fetch', mockFetch)

const baseDraft: Draft = {
  id: 'test_draft_123',
  pipeline: 'news_blog',
  created_at: '2026-11-09T10:00:00Z',
  stage_id: 'publish',
  endpoint_env: 'AILOOP_BLOG_WEBHOOK',
  inputs: { plan: 'Write about AI news' },
  all_outputs: { reviewed: 'AI news summary with sources' },
  filename: 'news_blog.123.draft.json',
  review_criteria: ['factual_sources_cited', 'no_prompt_leak'],
  review_reasoning: 'All criteria met. Sources are factual and no prompt leaked.',
}

describe('DraftReview', () => {
  const onClose = vi.fn()
  const onActionComplete = vi.fn()

  beforeEach(() => {
    mockFetch.mockReset()
    onClose.mockReset()
    onActionComplete.mockReset()
  })

  it('renders null when draft is null', () => {
    const { container } = render(
      <DraftReview draft={null} onClose={onClose} onActionComplete={onActionComplete} />
    )
    expect(container.firstChild).toBeNull()
  })

  it('displays pipeline outputs correctly', () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)

    // Use getAllByText because "AI news summary..." appears in both output and diff
    expect(screen.getAllByText('AI news summary with sources').length).toBeGreaterThanOrEqual(1)
    // "reviewed" appears as key in output section
    expect(screen.getAllByText('reviewed').length).toBeGreaterThanOrEqual(1)
  })

  it('shows review criteria with checkmarks when present', () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)

    expect(screen.getByText('Review criteria')).toBeInTheDocument()
    expect(screen.getByText('factual_sources_cited')).toBeInTheDocument()
    expect(screen.getByText('no_prompt_leak')).toBeInTheDocument()
    // Check for checkmark icons
    const icons = screen.getAllByText('✅')
    expect(icons.length).toBe(2)
  })

  it('shows reviewer reasoning when present', () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)

    expect(screen.getByText('Reviewer reasoning')).toBeInTheDocument()
    expect(screen.getByText(/All criteria met/)).toBeInTheDocument()
  })

  it('hides criteria section when review_criteria is empty', () => {
    const draftWithoutCriteria = { ...baseDraft, review_criteria: [] }
    render(<DraftReview draft={draftWithoutCriteria} onClose={onClose} onActionComplete={onActionComplete} />)

    expect(screen.queryByText('Review criteria')).not.toBeInTheDocument()
  })

  it('hides reasoning section when review_reasoning is empty', () => {
    const draftWithoutReasoning = { ...baseDraft, review_reasoning: '' }
    render(<DraftReview draft={draftWithoutReasoning} onClose={onClose} onActionComplete={onActionComplete} />)

    expect(screen.queryByText('Reviewer reasoning')).not.toBeInTheDocument()
  })

  it('shows diff view when both inputs and outputs exist', () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)

    expect(screen.getByText('Changes made')).toBeInTheDocument()
    expect(screen.getByText('Input')).toBeInTheDocument()
    expect(screen.getByText('Output')).toBeInTheDocument()
    expect(screen.getByText(/Write about AI news/)).toBeInTheDocument()
  })

  it('calls approve API when Approve button is clicked', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ status: 'published', draft_id: 'test_draft_123' }),
    })

    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)

    fireEvent.click(screen.getByRole('button', { name: /Approve & Publish/i }))

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/drafts/test_draft_123/approve',
        expect.objectContaining({ method: 'POST' })
      )
    })
  })

  it('calls reject API with reason when Reject button is clicked', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ status: 'rejected', draft_id: 'test_draft_123' }),
    })

    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)

    // Find textarea by placeholder
    const reasonInput = screen.getByPlaceholderText(/Explain why this draft should not be published/i)
    fireEvent.change(reasonInput, { target: { value: 'Quality issues' } })

    fireEvent.click(screen.getByRole('button', { name: /Reject/i }))

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/drafts/test_draft_123/reject',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ reason: 'Quality issues' }),
        })
      )
    })
  })
})