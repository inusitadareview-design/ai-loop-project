import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import DraftReview, { type Draft } from './DraftReview'

// Extract mocks via vi.hoisted so they survive vi.mock hoisting.
// This lets tests assert on the same fn instances the component uses.
const { mockApproveDraft, mockRejectDraft } = vi.hoisted(() => ({
  mockApproveDraft: vi.fn().mockResolvedValue(undefined),
  mockRejectDraft: vi.fn().mockResolvedValue(undefined),
}))

vi.mock('../hooks/useAPI', () => ({
  useAPI: () => ({
    approveDraft: mockApproveDraft,
    rejectDraft: mockRejectDraft,
  }),
}))

// Disable typewriter animation in tests (char-by-char render breaks
// synchronous getByText queries).
vi.mock('../hooks/useTypewriter', async () => {
  const actual = await vi.importActual('../hooks/useTypewriter')
  return {
    ...actual,
    getTypewriterPreference: () => false,
  }
})

describe('DraftReview', () => {
  const onClose = vi.fn()
  const onActionComplete = vi.fn()

  const baseDraft: Draft = {
    id: 'draft-1',
    pipeline: 'news_blog',
    created_at: '2026-11-09T10:00:00Z',
    stage_id: 'publish',
    filename: 'news_blog.123.draft.json',
    inputs: { raw: 'input data' },
    all_outputs: { post_aprovado: 'AI news summary with sources' },
    review_criteria: ['factual_sources_cited', 'no_prompt_leak'],
    review_reasoning: 'All criteria met. Sources are factual and no prompt leaked.',
  }

  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders null when draft is null', () => {
    const { container } = render(
      <DraftReview draft={null} onClose={onClose} onActionComplete={onActionComplete} />
    )
    expect(container.firstChild).toBeNull()
  })

  it('renders Quality Gate card with criteria and status badge', () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)
    expect(screen.getByText('Quality Gate')).toBeInTheDocument()
    expect(screen.getByText(/PASSED|NEEDS REVIEW/)).toBeInTheDocument()
    expect(screen.getByText('Factual Sources Cited')).toBeInTheDocument()
    expect(screen.getByText('No Prompt Leak')).toBeInTheDocument()
  })

  it('shows reviewer reasoning inside Quality Gate card', () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)
    expect(screen.getByText('Reviewer reasoning:')).toBeInTheDocument()
    expect(screen.getByText(/All criteria met/)).toBeInTheDocument()
  })

  it('renders single Generated content card with typewriter toggle', () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)
    expect(screen.getByText('Generated content')).toBeInTheDocument()
    expect(screen.getByText('✨ Typewriter')).toBeInTheDocument()
    expect(screen.getByText('AI news summary with sources')).toBeInTheDocument()
  })

  it('hides Quality Gate card when review_criteria is empty', () => {
    const draftWithoutCriteria: Draft = { ...baseDraft, review_criteria: [] }
    render(<DraftReview draft={draftWithoutCriteria} onClose={onClose} onActionComplete={onActionComplete} />)
    expect(screen.queryByText('Quality Gate')).not.toBeInTheDocument()
    expect(screen.queryByText('Factual Sources Cited')).not.toBeInTheDocument()
  })

  it('hides reasoning when review_reasoning is empty', () => {
    const draftWithoutReasoning: Draft = { ...baseDraft, review_reasoning: '' }
    render(<DraftReview draft={draftWithoutReasoning} onClose={onClose} onActionComplete={onActionComplete} />)
    expect(screen.queryByText('Reviewer reasoning:')).not.toBeInTheDocument()
  })

  it('calls approve API when Approve button is clicked', async () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)
    fireEvent.click(screen.getByText('Approve & Publish'))
    await waitFor(() => expect(mockApproveDraft).toHaveBeenCalledWith('draft-1'))
  })

  it('calls reject API with reason when Reject button is clicked', async () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)
    fireEvent.change(screen.getByPlaceholderText(/Explain why this draft should not be published/), {
      target: { value: 'Not relevant for today' },
    })
    fireEvent.click(screen.getByText('Reject'))
    await waitFor(() => expect(mockRejectDraft).toHaveBeenCalledWith('draft-1', 'Not relevant for today'))
  })

  it('shows error when rejecting without reason', async () => {
    render(<DraftReview draft={baseDraft} onClose={onClose} onActionComplete={onActionComplete} />)
    fireEvent.click(screen.getByText('Reject'))
    expect(await screen.findByText('Please provide a reason for rejection')).toBeInTheDocument()
  })
})