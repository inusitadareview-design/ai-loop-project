import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import PipelineCard, { type PipelineSummary } from './PipelineCard'

const mockFetch = vi.fn()
vi.stubGlobal('fetch', mockFetch)

const basePipeline: PipelineSummary = {
  name: 'news_blog',
  description: 'Coleta notícias de IA',
  scheduled: true,
  cron: '0 8 * * *',
  stages_count: 4,
  locked: false,
  paused: false,
}

describe('PipelineCard', () => {
  const onUpdate = vi.fn()
  const onViewMemory = vi.fn()

  beforeEach(() => {
    mockFetch.mockReset()
    onUpdate.mockReset()
    onViewMemory.mockReset()
  })

  it('renders pipeline info correctly', () => {
    render(
      <PipelineCard
        pipeline={basePipeline}
        onUpdate={onUpdate}
        onViewMemory={onViewMemory}
      />
    )
    expect(screen.getByText('news_blog')).toBeInTheDocument()
    expect(screen.getByText('Coleta notícias de IA')).toBeInTheDocument()
    expect(screen.getByText('Scheduled')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /run now/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /view memory/i })).toBeInTheDocument()
  })

  it('shows Resume button only when paused', () => {
    render(
      <PipelineCard
        pipeline={basePipeline}
        onUpdate={onUpdate}
        onViewMemory={onViewMemory}
      />
    )
    expect(screen.queryByRole('button', { name: /resume/i })).not.toBeInTheDocument()

    const paused = { ...basePipeline, paused: true }
    render(
      <PipelineCard
        pipeline={paused}
        onUpdate={onUpdate}
        onViewMemory={onViewMemory}
      />
    )
    expect(screen.getByRole('button', { name: /resume/i })).toBeInTheDocument()
  })

  it('disables Run now button when locked', () => {
    const locked = { ...basePipeline, locked: true }
    render(
      <PipelineCard
        pipeline={locked}
        onUpdate={onUpdate}
        onViewMemory={onViewMemory}
      />
    )
    expect(screen.getByRole('button', { name: /run now/i })).toBeDisabled()
  })

  it('calls POST /run when Run now is clicked', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ status: 'completed', pipeline: 'news_blog', outputs: {} }),
    })

    render(
      <PipelineCard
        pipeline={basePipeline}
        onUpdate={onUpdate}
        onViewMemory={onViewMemory}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /run now/i }))

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/pipelines/news_blog/run',
        expect.objectContaining({ method: 'POST' })
      )
    })

    await waitFor(() => {
      expect(onUpdate).toHaveBeenCalled()
    })

    expect(screen.getByText(/Completed: completed/i)).toBeInTheDocument()
  })

  it('calls DELETE /failures when Resume is clicked', async () => {
    const paused = { ...basePipeline, paused: true }
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ status: 'ok', pipeline: 'news_blog', resumed: true }),
    })

    render(
      <PipelineCard
        pipeline={paused}
        onUpdate={onUpdate}
        onViewMemory={onViewMemory}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /resume/i }))

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/pipelines/news_blog/failures',
        expect.objectContaining({ method: 'DELETE' })
      )
    })

    await waitFor(() => {
      expect(onUpdate).toHaveBeenCalled()
    })

    expect(screen.getByText(/Resumed successfully/i)).toBeInTheDocument()
  })

  it('shows error message when fetch fails', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 500,
      json: async () => ({ error: 'internal server error' }),
    })

    render(
      <PipelineCard
        pipeline={basePipeline}
        onUpdate={onUpdate}
        onViewMemory={onViewMemory}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /run now/i }))

    await waitFor(() => {
      expect(screen.getByText(/internal server error/i)).toBeInTheDocument()
    })
    expect(onUpdate).not.toHaveBeenCalled()
  })

  it('calls onViewMemory when View memory is clicked', () => {
    render(
      <PipelineCard
        pipeline={basePipeline}
        onUpdate={onUpdate}
        onViewMemory={onViewMemory}
      />
    )
    fireEvent.click(screen.getByRole('button', { name: /view memory/i }))
    expect(onViewMemory).toHaveBeenCalledWith('news_blog')
  })
})