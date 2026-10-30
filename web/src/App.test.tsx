import { render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import App from './App'

// Mock fetch globally
const mockFetch = vi.fn()
globalThis.fetch = mockFetch

describe('App', () => {
  beforeEach(() => {
    mockFetch.mockReset()
  })

  it('shows loading state initially', () => {
    // Mock both endpoints to return pending promises
    mockFetch.mockImplementation((url: string) => {
      if (url === '/version') {
        return Promise.resolve({
          ok: true,
          json: async () => ({ version: '1.0.0' }),
        })
      }
      if (url === '/api/v1/drafts') {
        return new Promise(() => {}) // pending
      }
      return new Promise(() => {}) // pending for pipelines
    })

    render(<App />)
    
    // Use more specific query to avoid ambiguity
    expect(screen.getByText('Loading...')).toBeInTheDocument()
  })

  it('renders pipelines after successful fetch', async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === '/version') {
        return Promise.resolve({
          ok: true,
          json: async () => ({ version: '1.7.4' }),
        })
      }
      if (url === '/api/v1/drafts') {
        return Promise.resolve({
          ok: true,
          json: async () => [],
        })
      }
      if (url === '/api/v1/pipelines') {
        return Promise.resolve({
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
      }
      return Promise.resolve({ ok: true, json: async () => ({}) })
    })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText('news_blog')).toBeInTheDocument()
      expect(screen.getByText('summarize')).toBeInTheDocument()
    })

    expect(screen.getByText('Coleta notícias de IA')).toBeInTheDocument()

    // "N stages" text is split across <strong> and a text node by React,
    // so getByText(/N stages/) won't find it. Use textContent instead.
    const stageItems = screen.getAllByText(/stages/)
    expect(stageItems).toHaveLength(2)
    expect(stageItems.some((el) => el.textContent === '4 stages')).toBe(true)
    expect(stageItems.some((el) => el.textContent === '2 stages')).toBe(true)

    expect(screen.getByText('Scheduled')).toBeInTheDocument()
    expect(screen.getByText('Paused')).toBeInTheDocument()
  })

  it('shows error state on fetch failure', async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === '/version') {
        return Promise.resolve({
          ok: true,
          json: async () => ({ version: '1.7.4' }),
        })
      }
      if (url === '/api/v1/drafts') {
        return Promise.resolve({
          ok: true,
          json: async () => [],
        })
      }
      if (url === '/api/v1/pipelines') {
        return Promise.resolve({
          ok: false,
          status: 500,
          json: async () => ({ error: 'Internal server error' }),
        })
      }
      return Promise.resolve({ ok: true, json: async () => ({}) })
    })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText(/Failed to load pipelines/)).toBeInTheDocument()
    })
  })

  it('shows empty state when no pipelines', async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === '/version') {
        return Promise.resolve({
          ok: true,
          json: async () => ({ version: '1.7.4' }),
        })
      }
      if (url === '/api/v1/drafts') {
        return Promise.resolve({
          ok: true,
          json: async () => [],
        })
      }
      if (url === '/api/v1/pipelines') {
        return Promise.resolve({
          ok: true,
          json: async () => [],
        })
      }
      return Promise.resolve({ ok: true, json: async () => ({}) })
    })

    render(<App />)

    await waitFor(() => {
      expect(screen.getByText(/No pipelines found/)).toBeInTheDocument()
    })
  })
})