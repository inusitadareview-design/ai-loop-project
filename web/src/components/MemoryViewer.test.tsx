import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import MemoryViewer from './MemoryViewer'

const mockFetch = vi.fn()
vi.stubGlobal('fetch', mockFetch)

describe('MemoryViewer', () => {
  beforeEach(() => {
    mockFetch.mockReset()
  })

  it('returns null when pipelineName is null', () => {
    const { container } = render(
      <MemoryViewer
        pipelineName={null}
        wsStatus="closed"
        liveEvents={[]}
        onClose={() => {}}
      />
    )
    expect(container.firstChild).toBeNull()
  })

  it('renders and fetches history on open', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        pipeline: 'news_blog',
        count: 2,
        events: [
          {
            id: 'e1',
            pipeline_id: 'news_blog',
            stage_id: 'plan',
            timestamp: 1000,
            type: 'stage_output',
            data: { msg: 'hello' },
          },
          {
            id: 'e2',
            pipeline_id: 'news_blog',
            timestamp: 2000,
            type: 'pipeline_complete',
            data: { status: 'ok' },
          },
        ],
      }),
    })

    render(
      <MemoryViewer
        pipelineName="news_blog"
        wsStatus="open"
        liveEvents={[]}
        onClose={() => {}}
      />
    )

    expect(screen.getByText(/Memory · news_blog/i)).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/pipelines/news_blog/memory?limit=50'
      )
    })

    await waitFor(() => {
      expect(screen.getByText('stage_output')).toBeInTheDocument()
      expect(screen.getByText('pipeline_complete')).toBeInTheDocument()
    })
  })

  it('filters live events to only the current pipeline', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ pipeline: 'news_blog', count: 0, events: [] }),
    })

    const live = [
      {
        type: 'pipeline_started',
        timestamp: '2026-10-30T10:00:00Z',
        payload: { pipeline: 'news_blog' },
      },
      {
        type: 'pipeline_started',
        timestamp: '2026-10-30T10:00:01Z',
        payload: { pipeline: 'other_pipeline' },
      },
      {
        type: 'pipeline_completed',
        timestamp: '2026-10-30T10:00:02Z',
        payload: { pipeline: 'news_blog', status: 'completed' },
      },
    ]

    render(
      <MemoryViewer
        pipelineName="news_blog"
        wsStatus="open"
        liveEvents={live}
        onClose={() => {}}
      />
    )

    await waitFor(() => {
      // The live count badge should show 2 (filtered).
      expect(screen.getByText('pipeline_started')).toBeInTheDocument()
      expect(screen.getByText('pipeline_completed')).toBeInTheDocument()
    })
  })

  it('shows error message on fetch failure', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 500,
      json: async () => ({}),
    })

    render(
      <MemoryViewer
        pipelineName="news_blog"
        wsStatus="closed"
        liveEvents={[]}
        onClose={() => {}}
      />
    )

    await waitFor(() => {
      expect(screen.getByText(/HTTP 500/i)).toBeInTheDocument()
    })
  })

  it('calls onClose when close button is clicked', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ pipeline: 'news_blog', count: 0, events: [] }),
    })
    const onClose = vi.fn()

    render(
      <MemoryViewer
        pipelineName="news_blog"
        wsStatus="open"
        liveEvents={[]}
        onClose={onClose}
      />
    )

    fireEvent.click(screen.getByLabelText(/close/i))
    expect(onClose).toHaveBeenCalled()
  })

  it('calls onClose on ESC key', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ pipeline: 'news_blog', count: 0, events: [] }),
    })
    const onClose = vi.fn()

    render(
      <MemoryViewer
        pipelineName="news_blog"
        wsStatus="open"
        liveEvents={[]}
        onClose={onClose}
      />
    )

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })

  it('shows wsStatus indicator correctly', () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ pipeline: 'news_blog', count: 0, events: [] }),
    })

    const { rerender } = render(
      <MemoryViewer
        pipelineName="news_blog"
        wsStatus="connecting"
        liveEvents={[]}
        onClose={() => {}}
      />
    )
    expect(screen.getByText(/Connecting/i)).toBeInTheDocument()

    rerender(
      <MemoryViewer
        pipelineName="news_blog"
        wsStatus="error"
        liveEvents={[]}
        onClose={() => {}}
      />
    )
    expect(screen.getByText('Error')).toBeInTheDocument()
  })
})