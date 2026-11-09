import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import CronJobManager from './CronJobManager'

const mockFetch = vi.fn()
vi.stubGlobal('fetch', mockFetch)

const scheduleResponse = {
  timezone: 'America/Sao_Paulo',
  pipelines: [
    {
      name: 'news_blog',
      cron: '0 8 * * *',
      enabled: true,
      next_execution: new Date(Date.now() + 2 * 60 * 60 * 1000).toISOString(),
      locked: false,
      paused: false,
    },
    {
      name: 'weekly_report',
      cron: '0 9 * * 1',
      enabled: false,
      next_execution: new Date(Date.now() + 3 * 24 * 60 * 60 * 1000).toISOString(),
      locked: false,
      paused: true,
    },
  ],
}

describe('CronJobManager', () => {
  beforeEach(() => {
    mockFetch.mockReset()
    mockFetch.mockImplementation((url: string, opts?: RequestInit) => {
      if (url === '/api/v1/schedule/status') {
        return Promise.resolve({
          ok: true,
          json: async () => scheduleResponse,
        })
      }
      if (typeof url === 'string' && url.includes('/schedule')) {
        const body = opts?.body ? JSON.parse(opts.body as string) : {}
        return Promise.resolve({
          ok: true,
          json: async () => ({ status: 'ok', pipeline: 'news_blog', enabled: body.enabled ?? true }),
        })
      }
      return Promise.resolve({ ok: true, json: async () => ({}) })
    })
  })

  it('renders empty state when no pipelines are scheduled', async () => {
    mockFetch.mockImplementation(() =>
      Promise.resolve({
        ok: true,
        json: async () => ({ timezone: 'UTC', pipelines: [] }),
      })
    )

    render(<CronJobManager />)
    await waitFor(() => {
      expect(screen.getByText(/No pipelines are currently scheduled/i)).toBeInTheDocument()
    })
  })

  it('renders scheduled pipelines with next execution', async () => {
    render(<CronJobManager />)

    await waitFor(() => {
      expect(screen.getByText('news_blog')).toBeInTheDocument()
      expect(screen.getByText('weekly_report')).toBeInTheDocument()
      expect(screen.getByText('0 8 * * *')).toBeInTheDocument()
      expect(screen.getByText('0 9 * * 1')).toBeInTheDocument()
    })

    expect(screen.getByText('Paused')).toBeInTheDocument()
  })

  it('displays timezone in header', async () => {
    render(<CronJobManager />)

    await waitFor(() => {
      expect(screen.getByText('America/Sao_Paulo')).toBeInTheDocument()
    })
  })

  it('calls PATCH to disable a pipeline when toggle is clicked', async () => {
    render(<CronJobManager />)

    await waitFor(() => {
      expect(screen.getByText('news_blog')).toBeInTheDocument()
    })

    const newsBlogToggle = screen.getByRole('checkbox', {
      name: /Disable schedule for news_blog/i,
    })
    fireEvent.click(newsBlogToggle)

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/pipelines/news_blog/schedule',
        expect.objectContaining({
          method: 'PATCH',
          body: JSON.stringify({ enabled: false }),
        })
      )
    })
  })

  it('calls PATCH to enable a pipeline when toggle is clicked', async () => {
    render(<CronJobManager />)

    await waitFor(() => {
      expect(screen.getByText('weekly_report')).toBeInTheDocument()
    })

    const weeklyToggle = screen.getByRole('checkbox', {
      name: /Enable schedule for weekly_report/i,
    })
    fireEvent.click(weeklyToggle)

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/pipelines/weekly_report/schedule',
        expect.objectContaining({
          method: 'PATCH',
          body: JSON.stringify({ enabled: true }),
        })
      )
    })
  })

  it('refresh button re-fetches schedule status', async () => {
    render(<CronJobManager />)

    await waitFor(() => {
      expect(screen.getByText('news_blog')).toBeInTheDocument()
    })

    const initialCalls = mockFetch.mock.calls.filter(
      (c) => c[0] === '/api/v1/schedule/status'
    ).length
    expect(initialCalls).toBe(1)

    fireEvent.click(screen.getByRole('button', { name: /refresh/i }))

    await waitFor(() => {
      const totalCalls = mockFetch.mock.calls.filter(
        (c) => c[0] === '/api/v1/schedule/status'
      ).length
      expect(totalCalls).toBe(2)
    })
  })

  it('shows error message when schedule fetch fails', async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === '/api/v1/schedule/status') {
        return Promise.resolve({
          ok: false,
          status: 500,
          json: async () => ({ error: 'internal server error' }),
        })
      }
      return Promise.resolve({ ok: true, json: async () => ({}) })
    })

    render(<CronJobManager />)

    await waitFor(() => {
      expect(screen.getByText(/internal server error/i)).toBeInTheDocument()
    })
  })
})
