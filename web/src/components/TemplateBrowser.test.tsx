import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import TemplateBrowser from './TemplateBrowser'

const mockFetch = vi.fn()
vi.stubGlobal('fetch', mockFetch)

const templatesList = [
  { name: 'news_blog_plan', path: 'internal/pipeline/templates/news_blog_plan.md', size_bytes: 620 },
  { name: 'weekly_report', path: 'internal/pipeline/templates/weekly_report.md', size_bytes: 450 },
]

const templateContent = {
  name: 'news_blog_plan',
  path: 'internal/pipeline/templates/news_blog_plan.md',
  content: 'Stage: {{.StageID}}\nModel: {{.Model}}\n\nWrite a plan.',
}

describe('TemplateBrowser', () => {
  const onSelect = vi.fn()
  const onClose = vi.fn()

  beforeEach(() => {
    mockFetch.mockReset()
    onSelect.mockReset()
    onClose.mockReset()
    mockFetch.mockImplementation((url: string) => {
      if (url === '/api/v1/templates') {
        return Promise.resolve({ ok: true, json: async () => templatesList })
      }
      if (url.includes('/templates/') && url.includes('/content')) {
        return Promise.resolve({ ok: true, json: async () => templateContent })
      }
      return Promise.resolve({ ok: true, json: async () => ({}) })
    })
  })

  it('renders empty state when no templates exist', async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === '/api/v1/templates') {
        return Promise.resolve({ ok: true, json: async () => [] })
      }
      return Promise.resolve({ ok: true, json: async () => ({}) })
    })

    render(<TemplateBrowser onSelect={onSelect} onClose={onClose} />)

    await waitFor(() => {
      expect(screen.getByText(/No templates available/i)).toBeInTheDocument()
    })
  })

  it('renders template list with name and size', async () => {
    render(<TemplateBrowser onSelect={onSelect} onClose={onClose} />)

    await waitFor(() => {
      // news_blog_plan appears both in list AND preview (auto-selected on mount)
      expect(screen.getAllByText('news_blog_plan').length).toBeGreaterThanOrEqual(1)
      expect(screen.getByText('weekly_report')).toBeInTheDocument()
      expect(screen.getByText('620 B')).toBeInTheDocument()
      expect(screen.getByText('450 B')).toBeInTheDocument()
    })
  })
  
  it('shows template preview when template is loaded', async () => {
    render(<TemplateBrowser onSelect={onSelect} onClose={onClose} />)

    await waitFor(() => {
      // Auto-selects first template on mount
      expect(screen.getByText(/Stage:/i)).toBeInTheDocument()
      expect(screen.getByText(/Model:/i)).toBeInTheDocument()
    })

    // Template variables are highlighted
    const variables = screen.getAllByText('{{.StageID}}')
    expect(variables.length).toBeGreaterThan(0)
  })

  it('filters templates by search term', async () => {
    render(<TemplateBrowser onSelect={onSelect} onClose={onClose} />)

    await waitFor(() => {
      expect(screen.getAllByText('news_blog_plan').length).toBeGreaterThanOrEqual(1)
      expect(screen.getByText('weekly_report')).toBeInTheDocument()
    })

    const searchInput = screen.getByPlaceholderText(/Search templates/i)
    fireEvent.change(searchInput, { target: { value: 'weekly' } })

    // After filter, news_blog_plan button should be gone from the list
    expect(screen.queryByRole('button', { name: /news_blog_plan/i })).not.toBeInTheDocument()
    expect(screen.getByText('weekly_report')).toBeInTheDocument()
  })

  it('calls onSelect with template path when Select is clicked', async () => {
    render(<TemplateBrowser onSelect={onSelect} onClose={onClose} />)

    await waitFor(() => {
      expect(screen.getAllByText('news_blog_plan').length).toBeGreaterThanOrEqual(1)
    })

    fireEvent.click(screen.getByRole('button', { name: /Select template/i }))

    expect(onSelect).toHaveBeenCalledWith('internal/pipeline/templates/news_blog_plan.md')
    expect(onClose).toHaveBeenCalled()
  })

  it('calls onClose when Cancel is clicked', async () => {
    render(<TemplateBrowser onSelect={onSelect} onClose={onClose} />)

    await waitFor(() => {
      expect(screen.getAllByText('news_blog_plan').length).toBeGreaterThanOrEqual(1)
    })

    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onClose).toHaveBeenCalled()
    expect(onSelect).not.toHaveBeenCalled()
  })
})