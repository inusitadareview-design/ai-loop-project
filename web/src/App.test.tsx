import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import App from './App'

// Mock fetch globally
const mockFetch = vi.fn()
globalThis.fetch = mockFetch

describe('App', () => {
  beforeEach(() => {
    mockFetch.mockReset()
    // Mock default: budget endpoint returns valid data
    mockFetch.mockImplementation((url: string) => {
      if (url === '/api/v1/budget') {
        return Promise.resolve({
          ok: true,
          json: async () => ({
            daily_limit_usd: 10.0,
            spent_usd: 0.5,
            remaining_usd: 9.5,
          }),
        })
      }
      // Other endpoints will be overridden in individual tests
      return Promise.resolve({
        ok: true,
        json: async () => ({}),
      })
    })
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

  it('shows error toast and does NOT open editor when edit API returns 404', async () => {
    // Mock all endpoints that App consumes on mount
    mockFetch.mockImplementation((url: string) => {
      // Pipelines list (needed by App on mount)
      if (url === '/api/v1/pipelines') {
        return Promise.resolve({
          ok: true,
          json: async () => [{ 
            name: 'news_blog', 
            description: '', 
            scheduled: false, 
            stages_count: 4, 
            locked: false, 
            paused: false 
          }],
        })
      }
      
      // Budget endpoint (BudgetCard on mount)
      if (url === '/api/v1/budget') {
        return Promise.resolve({
          ok: true,
          json: async () => ({ daily_limit_usd: 10, spent_usd: 0, remaining_usd: 10 }),
        })
      }
      
      // Drafts endpoint (DraftList on mount)
      if (url === '/api/v1/drafts') {
        return Promise.resolve({
          ok: true,
          json: async () => [],  // Empty array, not empty object
        })
      }
      
      // Schedule status (CronJobManager on mount)
      if (url === '/api/v1/schedule/status') {
        return Promise.resolve({
          ok: true,
          json: async () => ({ timezone: 'UTC', pipelines: [] }),
        })
      }
      
      // Edit endpoint returns 404
      if (url.includes('/api/v1/pipelines/news_blog') && !url.endsWith('/api/v1/pipelines')) {
        return Promise.resolve({
          ok: false,
          status: 404,
          json: async () => ({ error: 'pipeline "news_blog" not found' }),
        })
      }
      
      // Fallback for any other endpoint
      return Promise.resolve({ ok: true, json: async () => ({}) })
    })

    render(<App />)

    // Wait for pipelines to load
    await waitFor(() => {
      expect(screen.getByText('news_blog')).toBeInTheDocument()
    })

    // Click Edit button on the pipeline card
    const editButtons = screen.getAllByRole('button', { name: /edit/i })
    fireEvent.click(editButtons[0])

    // Should show error toast, NOT open the editor
    await waitFor(() => {
      expect(screen.getByText(/Failed to load pipeline/i)).toBeInTheDocument()
    })

    // Editor modal should NOT be present (would have "Create new pipeline" or "Edit:" title)
    expect(screen.queryByRole('dialog', { name: /create new pipeline/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: /edit:/i })).not.toBeInTheDocument()
  })
})