import { useCallback, useEffect, useMemo, useState } from 'react'
import { useAPI } from '../hooks/useAPI'
import type { TemplateSummary, TemplateContent } from '../types/pipeline'
import './TemplateBrowser.css'

interface TemplateBrowserProps {
  onSelect: (templatePath: string) => void
  onClose: () => void
}

export default function TemplateBrowser({ onSelect, onClose }: TemplateBrowserProps) {
  const [templates, setTemplates] = useState<TemplateSummary[]>([])
  const [selected, setSelected] = useState<TemplateContent | null>(null)
  const [search, setSearch] = useState('')
  const [status, setStatus] = useState<'idle' | 'loading-list' | 'loading-preview' | 'ok' | 'error'>('idle')
  const [error, setError] = useState<string | null>(null)

  const { listTemplates, getTemplateContent } = useAPI()

  // Load templates on mount
  useEffect(() => {
    setStatus('loading-list')
    listTemplates()
      .then((data) => {
        setTemplates(data)
        setStatus('ok')
        // Auto-select first template if any
        if (data.length > 0) {
          void loadPreview(data[0].name)
        }
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : 'Failed to load templates')
        setStatus('error')
      })
  }, [listTemplates])

  // ESC to close
  useEffect(() => {
    const handleEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handleEsc)
    return () => window.removeEventListener('keydown', handleEsc)
  }, [onClose])

  const loadPreview = useCallback(async (name: string) => {
    setStatus('loading-preview')
    setError(null)
    try {
      const content = await getTemplateContent(name)
      setSelected(content)
      setStatus('ok')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load preview')
      setStatus('error')
    }
  }, [getTemplateContent])

  // Filter templates by search term
  const filteredTemplates = useMemo(() => {
    if (!search.trim()) return templates
    const term = search.toLowerCase()
    return templates.filter((t) => t.name.toLowerCase().includes(term))
  }, [templates, search])

  // Highlight {{.Variable}} syntax in preview
  const renderPreview = (content: string): React.ReactNode => {
    if (!content) return <span className="template-preview-empty">Empty template</span>
    // Split on template variable pattern and highlight
    const parts = content.split(/({{\.[A-Za-z0-9_.]+}})/g)
    return parts.map((part, i) => {
      if (/^{{\.[A-Za-z0-9_.]+}}$/.test(part)) {
        return (
          <span key={i} className="template-variable" title="Template variable">
            {part}
          </span>
        )
      }
      return <span key={i}>{part}</span>
    })
  }

  const formatBytes = (bytes: number): string => {
    if (bytes < 1024) return `${bytes} B`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  }

  const handleConfirm = () => {
    if (selected) {
      onSelect(selected.path)
      onClose()
    }
  }

  return (
    <>
      <div className="template-backdrop" onClick={onClose} />
      <div className="template-modal" role="dialog" aria-modal="true" aria-labelledby="template-title">
        <header className="template-header">
          <div>
            <h2 className="template-title" id="template-title">
              📄 Browse templates
            </h2>
            <p className="template-subtitle">
              Select a prompt template for this stage
            </p>
          </div>
          <button type="button" className="btn btn--ghost" onClick={onClose} aria-label="Close">
            ×
          </button>
        </header>

        <div className="template-search">
          <input
            type="text"
            placeholder="Search templates..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            aria-label="Search templates"
          />
        </div>

        <div className="template-body">
          <aside className="template-list" aria-label="Template list">
            {status === 'loading-list' && (
              <p className="template-status">Loading templates...</p>
            )}
            {status === 'error' && error && !selected && (
              <div className="template-error" role="alert">{error}</div>
            )}
            {status === 'ok' && filteredTemplates.length === 0 && (
              <p className="template-status">
                {search ? 'No templates match your search.' : 'No templates available.'}
              </p>
            )}
            {filteredTemplates.map((t) => (
              <button
                key={t.path}
                type="button"
                className={`template-list-item ${selected?.name === t.name ? 'template-list-item--active' : ''}`}
                onClick={() => loadPreview(t.name)}
              >
                <div className="template-list-item-name">{t.name}</div>
                <div className="template-list-item-meta">{formatBytes(t.size_bytes)}</div>
              </button>
            ))}
          </aside>

          <section className="template-preview" aria-label="Template preview">
            {status === 'loading-preview' && (
              <p className="template-status">Loading preview...</p>
            )}
            {error && selected && (
              <div className="template-error" role="alert">{error}</div>
            )}
            {selected && (
              <>
                <div className="template-preview-header">
                  <h3 className="template-preview-title">{selected.name}</h3>
                  <code className="template-preview-path">{selected.path}</code>
                </div>
                <pre className="template-preview-content">
                  <code>{renderPreview(selected.content)}</code>
                </pre>
                <div className="template-preview-legend">
                  <span className="template-variable">{'{{.Variable}}'}</span>
                  <span>= template variable (substituted at runtime)</span>
                </div>
              </>
            )}
            {!selected && status === 'ok' && (
              <p className="template-status">Select a template to preview</p>
            )}
          </section>
        </div>

        <footer className="template-footer">
          <button type="button" className="btn btn--ghost" onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn--primary"
            onClick={handleConfirm}
            disabled={!selected || status === 'loading-preview'}
          >
            Select template
          </button>
        </footer>
      </div>
    </>
  )
}