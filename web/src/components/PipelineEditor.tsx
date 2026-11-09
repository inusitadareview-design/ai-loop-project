import { useCallback, useEffect, useState } from 'react'
import { useAPI } from '../hooks/useAPI'
import type { Pipeline, Stage, TemplateSummary } from '../types/pipeline'
import TemplateBrowser from './TemplateBrowser'
import './PipelineEditor.css'

interface PipelineEditorProps {
  pipeline?: Pipeline | null // null/undefined = create mode, Pipeline = edit mode
  onClose: () => void
  onSaved: () => void
}

// Regex para validação de nome (G2.2: previne path traversal)
const NAME_REGEX = /^[a-z0-9][a-z0-9_-]{0,63}$/

const ACTION_OPTIONS = ['generate', 'self_review', 'http_post'] as const

const DEFAULT_STAGE: Stage = {
  id: '',
  action: 'generate',
  depends_on: [],
  prompt_file: '',
  input_keys: [],
  output_key: '',
  criteria: [],
  endpoint_env: '',
}

const DEFAULT_PIPELINE: Pipeline = {
  name: '',
  description: '',
  schedule: { enabled: false, cron: '' },
  model: {
    preferred: 'omniroute/llama-3.8b-instruct',
    fallbacks: [],
    max_tokens: 2048,
    temperature: 0.4,
  },
  stages: [
    { id: 'plan', action: 'generate', output_key: 'plan' },
    { id: 'execute', action: 'generate', depends_on: ['plan'], output_key: 'draft' },
    { id: 'review', action: 'self_review', depends_on: ['execute'], criteria: ['no_prompt_leak'], output_key: 'reviewed' },
    { id: 'publish', action: 'http_post', depends_on: ['review'], endpoint_env: '', input_keys: ['reviewed'] },
  ],
  limits: {
    require_review: true,
    max_cost_per_run_usd: 0.0,
    max_retries_per_stage: 3,
    dead_loop_failures: 3,
  },
  cache: { enabled: true, ttl_minutes: 240 },
}

type ActionStatus = 'idle' | 'saving' | 'success' | 'error'

export default function PipelineEditor({ pipeline, onClose, onSaved }: PipelineEditorProps) {
  const isEditMode = Boolean(pipeline)
  const [form, setForm] = useState<Pipeline>(pipeline ?? DEFAULT_PIPELINE)
const [browseStageIndex, setBrowseStageIndex] = useState<number | null>(null)
  const [status, setStatus] = useState<ActionStatus>('idle')
  const [error, setError] = useState<string | null>(null)
  const [templates, setTemplates] = useState<TemplateSummary[]>([])
  const [activeSection, setActiveSection] = useState<string>('basic')
  const { createPipeline, updatePipeline, listTemplates } = useAPI()

  // Load templates on mount
  useEffect(() => {
    listTemplates()
      .then(setTemplates)
      .catch(() => setTemplates([])) // non-fatal: empty dropdown
  }, [listTemplates])

  // ESC to close
  useEffect(() => {
    const handleEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handleEsc)
    return () => window.removeEventListener('keydown', handleEsc)
  }, [onClose])

  const updateField = useCallback(<K extends keyof Pipeline>(field: K, value: Pipeline[K]) => {
    setForm((prev) => ({ ...prev, [field]: value }))
  }, [])

  const updateStage = useCallback((index: number, field: keyof Stage, value: any) => {
    setForm((prev) => {
      const stages = [...prev.stages]
      stages[index] = { ...stages[index], [field]: value }
      return { ...prev, stages }
    })
  }, [])

  const addStage = useCallback(() => {
    setForm((prev) => ({
      ...prev,
      stages: [...prev.stages, { ...DEFAULT_STAGE, id: `stage_${prev.stages.length + 1}` }],
    }))
  }, [])

  const removeStage = useCallback((index: number) => {
    setForm((prev) => ({
      ...prev,
      stages: prev.stages.filter((_, i) => i !== index),
    }))
  }, [])

  const addFallback = useCallback(() => {
    setForm((prev) => ({
      ...prev,
      model: { ...prev.model, fallbacks: [...(prev.model.fallbacks ?? []), ''] },
    }))
  }, [])

  const updateFallback = useCallback((index: number, value: string) => {
    setForm((prev) => {
      const fallbacks = [...(prev.model.fallbacks ?? [])]
      fallbacks[index] = value
      return { ...prev, model: { ...prev.model, fallbacks } }
    })
  }, [])

  const removeFallback = useCallback((index: number) => {
    setForm((prev) => ({
      ...prev,
      model: { ...prev.model, fallbacks: (prev.model.fallbacks ?? []).filter((_, i) => i !== index) },
    }))
  }, [])

  // Client-side validation (G2.2)
  const validate = (): string | null => {
    if (!NAME_REGEX.test(form.name)) {
      return 'Name must match: lowercase alphanumeric + underscore/hyphen, max 64 chars'
    }
    if (form.stages.length === 0) {
      return 'At least one stage is required'
    }
    const stageIds = new Set<string>()
    for (const s of form.stages) {
      if (!s.id) return 'All stages must have an ID'
      if (stageIds.has(s.id)) return `Duplicate stage ID: ${s.id}`
      stageIds.add(s.id)
      if (!s.action) return `Stage ${s.id}: action is required`
    }
    // Validate depends_on references
    for (const s of form.stages) {
      for (const dep of s.depends_on ?? []) {
        if (!stageIds.has(dep)) {
          return `Stage ${s.id}: depends on unknown stage "${dep}"`
        }
      }
    }
    return null
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(null)

    const validationError = validate()
    if (validationError) {
      setError(validationError)
      return
    }

    setStatus('saving')
    try {
      if (isEditMode) {
        await updatePipeline(form.name, form)
      } else {
        await createPipeline(form)
      }
      setStatus('success')
      onSaved()
      onClose()
    } catch (err) {
      setStatus('error')
      setError(err instanceof Error ? err.message : 'Unknown error')
    }
  }

  const sections = [
    { id: 'basic', label: 'Basic' },
    { id: 'schedule', label: 'Schedule' },
    { id: 'model', label: 'Model' },
    { id: 'stages', label: `Stages (${form.stages.length})` },
    { id: 'limits', label: 'Limits' },
    { id: 'cache', label: 'Cache' },
  ]

  const templateOptions = templates.map((t) => t.path)

  return (
    <>
      <div className="editor-backdrop" onClick={onClose} />
      <div className="editor-modal" role="dialog" aria-modal="true" aria-labelledby="editor-title">
        <header className="editor-header">
          <div>
            <h2 className="editor-title" id="editor-title">
              {isEditMode ? `Edit: ${form.name}` : 'Create new pipeline'}
            </h2>
            <p className="editor-subtitle">
              {isEditMode ? 'Update pipeline configuration' : 'Define a new autonomous AI pipeline'}
            </p>
          </div>
          <button type="button" className="btn btn--ghost" onClick={onClose} aria-label="Close">
            ×
          </button>
        </header>

        <nav className="editor-nav">
          {sections.map((s) => (
            <button
              key={s.id}
              type="button"
              className={`editor-nav-item ${activeSection === s.id ? 'editor-nav-item--active' : ''}`}
              onClick={() => setActiveSection(s.id)}
            >
              {s.label}
            </button>
          ))}
        </nav>

        <form className="editor-body" onSubmit={handleSubmit}>
          {error && (
            <div className="editor-error" role="alert">
              {error}
            </div>
          )}

          {activeSection === 'basic' && (
            <section className="editor-section">
              <div className="form-field">
                <label htmlFor="name">Pipeline name</label>
                <input
                  id="name"
                  type="text"
                  value={form.name}
                  onChange={(e) => updateField('name', e.target.value)}
                  disabled={isEditMode}
                  placeholder="e.g. news_blog"
                  pattern="^[a-z0-9][a-z0-9_-]{0,63}$"
                  required
                />
                <small className="form-help">Lowercase, numbers, underscore, hyphen. Max 64 chars.</small>
              </div>
              <div className="form-field">
                <label htmlFor="description">Description</label>
                <textarea
                  id="description"
                  value={form.description}
                  onChange={(e) => updateField('description', e.target.value)}
                  placeholder="What does this pipeline do?"
                  rows={3}
                />
              </div>
            </section>
          )}

          {activeSection === 'schedule' && (
            <section className="editor-section">
              <div className="form-field form-field--checkbox">
                <label>
                  <input
                    type="checkbox"
                    checked={form.schedule.enabled}
                    onChange={(e) => updateField('schedule', { ...form.schedule, enabled: e.target.checked })}
                  />
                  Enable scheduled execution
                </label>
              </div>
              {form.schedule.enabled && (
                <div className="form-field">
                  <label htmlFor="cron">Cron expression</label>
                  <input
                    id="cron"
                    type="text"
                    value={form.schedule.cron}
                    onChange={(e) => updateField('schedule', { ...form.schedule, cron: e.target.value })}
                    placeholder="0 8 * * *"
                  />
                  <small className="form-help">
                    Standard cron syntax. Example: <code>0 8 * * *</code> = daily at 08:00
                  </small>
                </div>
              )}
            </section>
          )}

          {activeSection === 'model' && (
            <section className="editor-section">
              <div className="form-field">
                <label htmlFor="preferred">Preferred model</label>
                <input
                  id="preferred"
                  type="text"
                  value={form.model.preferred}
                  onChange={(e) => updateField('model', { ...form.model, preferred: e.target.value })}
                  placeholder="omniroute/llama-3.8b-instruct"
                  required
                />
                </div>
                <div className="form-field">
                    <label>Fallback models</label>
                    {(form.model.fallbacks ?? []).map((fb, i) => (
                    <div key={i} className="form-field-row">
                        <input
                        type="text"
                        value={fb}
                        onChange={(e) => updateFallback(i, e.target.value)}
                        placeholder="openrouter/meta-llama/llama-3.1-8b-instruct:free"
                        />
                        <button type="button" className="btn btn--ghost" onClick={() => removeFallback(i)}>
                        Remove
                        </button>
                    </div>
                    ))}
                    <button type="button" className="btn btn--ghost btn--small" onClick={addFallback}>
                    + Add fallback
                    </button>
                </div>
                <div className="form-field-row form-field-row--two-col">
                    <div className="form-field">
                        <label htmlFor="max_tokens">Max tokens</label>
                        <input
                            id="max_tokens"
                            type="number"
                            min={1}
                            max={32000}
                            value={form.model.max_tokens}
                            onChange={(e) => updateField('model', { ...form.model, max_tokens: parseInt(e.target.value) || 0 })}
                        />
                    </div>
                <div className="form-field">
                    <label htmlFor="temperature">Temperature</label>
                    <input
                        id="temperature"
                        type="number"
                        step={0.1}
                        min={0}
                        max={2}
                        value={form.model.temperature}
                        onChange={(e) => updateField('model', { ...form.model, temperature: parseFloat(e.target.value) || 0 })}
                    />
                </div>
              </div>
            </section>
          )}

          {activeSection === 'stages' && (
            <section className="editor-section">
              {form.stages.map((stage, i) => (
                <div key={i} className="stage-card">
                    <div className="stage-card-header">
                        <h3 className="stage-card-title">Stage {i + 1}</h3>
                        <button
                        type="button"
                        className="btn btn--ghost btn--small btn--danger-text"
                        onClick={() => removeStage(i)}
                        disabled={form.stages.length === 1}
                        >
                        Remove
                        </button>
                    </div>

                    <div className="form-field-row form-field-row--two-col">
                        <div className="form-field">
                        <label>ID</label>
                        <input
                            type="text"
                            value={stage.id}
                            onChange={(e) => updateStage(i, 'id', e.target.value)}
                            placeholder="plan"
                            required
                        />
                        </div>
                        <div className="form-field">
                        <label>Action</label>
                        <select
                            value={stage.action}
                            onChange={(e) => updateStage(i, 'action', e.target.value)}
                        >
                            {ACTION_OPTIONS.map((a) => (
                            <option key={a} value={a}>{a}</option>
                            ))}
                        </select>
                        </div>
                    </div>

                    <div className="form-field">
                        <label>Prompt file (template)</label>
                        <div className="form-field-row">
                        <select
                            value={stage.prompt_file ?? ''}
                            onChange={(e) => updateStage(i, 'prompt_file', e.target.value)}
                            style={{ flex: 1 }}
                        >
                            <option value="">(none — use hardcoded prompt)</option>
                            {templateOptions.map((t) => (
                            <option key={t} value={t}>{t}</option>
                            ))}
                        </select>
                        <button
                            type="button"
                            className="btn btn--ghost btn--small"
                            onClick={() => setBrowseStageIndex(i)}
                            title="Browse and preview templates"
                        >
                            📄 Browse
                        </button>
                        </div>
                    </div>

                    <div className="form-field">
                        <label>Depends on (comma-separated stage IDs)</label>
                        <input
                        type="text"
                        value={(stage.depends_on ?? []).join(', ')}
                        onChange={(e) => updateStage(i, 'depends_on', e.target.value.split(',').map((s) => s.trim()).filter(Boolean))}
                        placeholder="plan, execute"
                        />
                    </div>

                    <div className="form-field-row form-field-row--two-col">
                        <div className="form-field">
                        <label>Input keys (comma-separated)</label>
                        <input
                            type="text"
                            value={(stage.input_keys ?? []).join(', ')}
                            onChange={(e) => updateStage(i, 'input_keys', e.target.value.split(',').map((s) => s.trim()).filter(Boolean))}
                            placeholder="plan"
                        />
                        </div>
                        <div className="form-field">
                        <label>Output key</label>
                        <input
                            type="text"
                            value={stage.output_key ?? ''}
                            onChange={(e) => updateStage(i, 'output_key', e.target.value)}
                            placeholder="draft"
                        />
                        </div>
                    </div>

                    {stage.action === 'self_review' && (
                        <div className="form-field">
                        <label>Review criteria (comma-separated)</label>
                        <input
                            type="text"
                            value={(stage.criteria ?? []).join(', ')}
                            onChange={(e) => updateStage(i, 'criteria', e.target.value.split(',').map((s) => s.trim()).filter(Boolean))}
                            placeholder="factual_sources_cited, no_prompt_leak"
                        />
                        <small className="form-help">Criteria the LLM reviewer will check.</small>
                        </div>
                    )}

                    {stage.action === 'http_post' && (
                        <div className="form-field">
                        <label>Webhook env var name</label>
                        <input
                            type="text"
                            value={stage.endpoint_env ?? ''}
                            onChange={(e) => updateStage(i, 'endpoint_env', e.target.value)}
                            placeholder="AILOOP_BLOG_WEBHOOK"
                        />
                        <small className="form-help">Environment variable containing the webhook URL (G2.1).</small>
                        </div>
                    )}
                    </div>
                ))}

                <button type="button" className="btn btn--ghost" onClick={addStage}>
                    + Add stage
                </button>

                {browseStageIndex !== null && (
                    <TemplateBrowser
                    onSelect={(path) => {
                        updateStage(browseStageIndex, 'prompt_file', path)
                        setBrowseStageIndex(null)
                    }}
                    onClose={() => setBrowseStageIndex(null)}
                    />
                )}
            </section>
          )}

          {activeSection === 'limits' && (
            <section className="editor-section">
              <div className="form-field form-field--checkbox">
                <label>
                  <input
                    type="checkbox"
                    checked={form.limits.require_review}
                    onChange={(e) => updateField('limits', { ...form.limits, require_review: e.target.checked })}
                  />
                  Require human approval before publishing (G3.2)
                </label>
                <small className="form-help">
                  When enabled, http_post stages save drafts for review instead of publishing immediately.
                </small>
              </div>
              <div className="form-field-row form-field-row--two-col">
                <div className="form-field">
                  <label>Max cost per run (USD)</label>
                  <input
                    type="number"
                    step={0.01}
                    min={0}
                    value={form.limits.max_cost_per_run_usd}
                    onChange={(e) => updateField('limits', { ...form.limits, max_cost_per_run_usd: parseFloat(e.target.value) || 0 })}
                  />
                </div>
                <div className="form-field">
                  <label>Max retries per stage</label>
                  <input
                    type="number"
                    min={0}
                    max={10}
                    value={form.limits.max_retries_per_stage}
                    onChange={(e) => updateField('limits', { ...form.limits, max_retries_per_stage: parseInt(e.target.value) || 0 })}
                  />
                </div>
              </div>
              <div className="form-field">
                <label>Dead-loop failures threshold</label>
                <input
                  type="number"
                  min={1}
                  max={100}
                  value={form.limits.dead_loop_failures}
                  onChange={(e) => updateField('limits', { ...form.limits, dead_loop_failures: parseInt(e.target.value) || 0 })}
                />
                <small className="form-help">
                  After this many consecutive failures, pipeline is paused (G4.1).
                </small>
              </div>
            </section>
          )}

          {activeSection === 'cache' && (
            <section className="editor-section">
              <div className="form-field form-field--checkbox">
                <label>
                  <input
                    type="checkbox"
                    checked={form.cache.enabled}
                    onChange={(e) => updateField('cache', { ...form.cache, enabled: e.target.checked })}
                  />
                  Enable response cache (D3)
                </label>
                <small className="form-help">
                  Cache hit = cost $0. Repeated identical requests serve from disk.
                </small>
              </div>
              {form.cache.enabled && (
                <div className="form-field">
                  <label>TTL (minutes)</label>
                  <input
                    type="number"
                    min={0}
                    max={10080}
                    value={form.cache.ttl_minutes}
                    onChange={(e) => updateField('cache', { ...form.cache, ttl_minutes: parseInt(e.target.value) || 0 })}
                  />
                  <small className="form-help">How long cached responses remain valid. 0 = expire immediately.</small>
                </div>
              )}
            </section>
          )}
        </form>

        <footer className="editor-footer">
          <button type="button" className="btn btn--ghost" onClick={onClose} disabled={status === 'saving'}>
            Cancel
          </button>
          <button
            type="submit"
            className={`btn btn--primary ${status === 'saving' ? 'btn--loading' : ''}`}
            onClick={handleSubmit}
            disabled={status === 'saving'}
          >
            {status === 'saving'
              ? 'Saving...'
              : isEditMode
              ? 'Update pipeline'
              : 'Create pipeline'}
          </button>
        </footer>
      </div>
    </>
  )
}