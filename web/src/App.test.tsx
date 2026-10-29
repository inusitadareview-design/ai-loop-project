import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import App from './App'

// Mock the fetch API globally for tests.
const mockFetch = vi.fn()
vi.stubGlobal('fetch', mockFetch)

describe('App', () => {
  beforeEach(() => {
    mockFetch.mockReset()
  })

  it('shows loading state initially', () => {
    // Keep fetch pending so loading state stays visible.
    mockFetch.mockImplementation(() => new Promise(() => {}))
    render(<App />)
    expect(screen.getByText(/Loading/i)).toBeInTheDocument()
  })

  it('renders pipelines after successful fetch', async () => {
    mockFetch
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ version: '1.7.4' }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => [
          {
            name: 'news_blog',
            description: 'Coleta notícias de IA',
            scheduled: true,
            cron: '0 8 * * *',
            stages_count: 4,
            locked: false,
            paused: false,
          },
          {
            name: 'summarize',
            description: 'Resume artigos',
            scheduled: false,
            stages_count: 2,
            locked: false,
            paused: true,
          },
        ],
      })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText('news_blog')).toBeInTheDocument()
      expect(screen.getByText('summarize')).toBeInTheDocument()
    })

    expect(screen.getByText('Coleta notícias de IA')).toBeInTheDocument()

    // The "4 stages" / "2 stages" text is split across <strong> and a
    // text node by React, so getByText(/N stages/) won't find it.
    // Use textContent on the collected meta-items instead.
    const stageItems = screen.getAllByText(/stages/)
    expect(stageItems).toHaveLength(2)
    expect(stageItems.some((el) => el.textContent === '4 stages')).toBe(true)
    expect(stageItems.some((el) => el.textContent === '2 stages')).toBe(true)

    expect(screen.getByText('Scheduled')).toBeInTheDocument()
    expect(screen.getByText('Paused')).toBeInTheDocument()
  })

  it('shows error state on fetch failure', async () => {
    mockFetch
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ version: '1.7.4' }),
      })
      .mockResolvedValueOnce({
        ok: false,
        status: 500,
        json: async () => ({}),
      })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText(/Failed to load pipelines/)).toBeInTheDocument()
    })
    expect(screen.getByText(/Disconnected/)).toBeInTheDocument()
  })

  it('shows empty state when no pipelines', async () => {
    mockFetch
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ version: '1.7.4' }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => [],
      })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText(/No pipelines found/)).toBeInTheDocument()
    })
  })
})