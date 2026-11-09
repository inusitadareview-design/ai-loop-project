// useAPI.ts - HTTP client for REST API (self-hosted model, browser-based).
// Replaces useWailsAPI after Wails abandonment. All communication via fetch().

import { useCallback } from 'react'

export interface PipelineSummary {
  name: string
  description: string
  scheduled: boolean
  cron?: string
  stages_count: number
  locked: boolean
  paused: boolean
}

export interface PipelineResult {
  pipeline_name: string
  status: string
  outputs?: Record<string, string>
  error?: string
}

export interface MemoryEvent {
  id: string
  pipeline_id: string
  stage_id?: string
  timestamp: number
  type: string
  data?: Record<string, unknown>
}

async function fetchJSON<T>(url: string, options?: RequestInit): Promise<T> {
  const res = await fetch(url, options)
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(body.error ?? `HTTP ${res.status}`)
  }
  return res.json()
}

export function useAPI() {
  const getDrafts = useCallback(async (): Promise<any[]> => {
    return fetchJSON('/api/v1/drafts')
  }, [])

  const approveDraft = useCallback(async (id: string): Promise<void> => {
    await fetchJSON(`/api/v1/drafts/${id}/approve`, { method: 'POST' })
  }, [])

  const rejectDraft = useCallback(async (id: string, reason: string): Promise<void> => {
    await fetchJSON(`/api/v1/drafts/${id}/reject`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ reason })
    })
  }, [])

  const listPipelines = useCallback(async (): Promise<PipelineSummary[]> => {
    return fetchJSON('/api/v1/pipelines')
  }, [])

  const runPipeline = useCallback(async (name: string): Promise<PipelineResult> => {
    return fetchJSON(`/api/v1/pipelines/${name}/run`, { method: 'POST' })
  }, [])

  const resetPipelineFailures = useCallback(async (name: string): Promise<boolean> => {
    const data = await fetchJSON<{ resumed: boolean }>(
      `/api/v1/pipelines/${name}/failures`,
      { method: 'DELETE' }
    )
    return data.resumed
  }, [])

  const getPipelineMemory = useCallback(async (name: string, limit: number = 50): Promise<MemoryEvent[]> => {
    const body = await fetchJSON<{ events?: MemoryEvent[] }>(
      `/api/v1/pipelines/${encodeURIComponent(name)}/memory?limit=${limit}`
    )
    return Array.isArray(body.events) ? body.events : []
  }, [])

  // Dentro do hook useAPI, adicione esta função e exporte-a
  const getBudget = useCallback(async (): Promise<{
    daily_limit_usd: number
    spent_usd: number
    remaining_usd: number
  }> => {
    const res = await fetch('/api/v1/budget')
    if (!res.ok) {
      throw new Error(`Failed to fetch budget: ${res.status}`)
    }
    return res.json()
  }, [])

    const createPipeline = useCallback(async (pipeline: import('../types/pipeline').Pipeline): Promise<import('../types/pipeline').Pipeline> => {
    return fetchJSON('/api/v1/pipelines', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(pipeline)
    })
  }, [])

  const updatePipeline = useCallback(async (name: string, pipeline: import('../types/pipeline').Pipeline): Promise<import('../types/pipeline').Pipeline> => {
    return fetchJSON(`/api/v1/pipelines/${encodeURIComponent(name)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(pipeline)
    })
  }, [])

  const deletePipeline = useCallback(async (name: string): Promise<void> => {
    await fetchJSON(`/api/v1/pipelines/${encodeURIComponent(name)}`, {
      method: 'DELETE'
    })
  }, [])

  const listTemplates = useCallback(async (): Promise<import('../types/pipeline').TemplateSummary[]> => {
    return fetchJSON('/api/v1/templates')
  }, [])

  const getScheduleStatus = useCallback(async (): Promise<import('../types/pipeline').ScheduleStatus> => {
    return fetchJSON('/api/v1/schedule/status')
  }, [])

  const updatePipelineSchedule = useCallback(async (name: string, enabled: boolean): Promise<{ status: string; pipeline: string; enabled: boolean }> => {
    return fetchJSON(`/api/v1/pipelines/${encodeURIComponent(name)}/schedule`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled })
    })
  }, [])

  return {
    listPipelines,
    runPipeline,
    resetPipelineFailures,
    getPipelineMemory,
    getDrafts,
    approveDraft,
    rejectDraft,
    getBudget,
    createPipeline,
    updatePipeline,
    deletePipeline,
    listTemplates,
    getScheduleStatus,
    updatePipelineSchedule,
  }   
}
