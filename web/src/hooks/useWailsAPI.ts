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

interface WailsRuntime {
  go: {
    main: {
      App: {
        GetVersion(): Promise<string>
        GetDrafts(): Promise<any[]>
        ApproveDraft(id: string): Promise<void>
        RejectDraft(id: string, reason: string): Promise<void>
        ListPipelines(): Promise<PipelineSummary[]>
        RunPipeline(name: string): Promise<PipelineResult>
        ResetPipelineFailures(name: string): Promise<boolean>
        GetPipelineMemory(name: string, limit: number): Promise<MemoryEvent[]>
      }
    }
  }
  runtime: {
    EventsOn(eventName: string, callback: (data: any) => void): void
    EventsOff(eventName: string): void
  }
}

declare global {
  interface Window {
    go?: WailsRuntime['go']
    runtime?: WailsRuntime['runtime']
  }
}

export function useWailsAPI() {
  const isWailsAvailable = useCallback(() => {
    return typeof window !== 'undefined' && window.go?.main?.App
  }, [])

  const getDrafts = useCallback(async (): Promise<any[]> => {
    if (!isWailsAvailable()) {
      const response = await fetch('/api/v1/drafts')
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      return response.json()
    }
    return window.go!.main.App.GetDrafts()
  }, [isWailsAvailable])

  const approveDraft = useCallback(async (id: string): Promise<void> => {
    if (!isWailsAvailable()) {
      const response = await fetch(`/api/v1/drafts/${id}/approve`, { method: 'POST' })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      return
    }
    return window.go!.main.App.ApproveDraft(id)
  }, [isWailsAvailable])

  const rejectDraft = useCallback(async (id: string, reason: string): Promise<void> => {
    if (!isWailsAvailable()) {
      const response = await fetch(`/api/v1/drafts/${id}/reject`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reason })
      })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      return
    }
    return window.go!.main.App.RejectDraft(id, reason)
  }, [isWailsAvailable])

  const listPipelines = useCallback(async (): Promise<PipelineSummary[]> => {
    if (!isWailsAvailable()) {
      const response = await fetch('/api/v1/pipelines')
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      return response.json()
    }
    return window.go!.main.App.ListPipelines()
  }, [isWailsAvailable])

  const runPipeline = useCallback(async (name: string): Promise<PipelineResult> => {
    if (!isWailsAvailable()) {
      const response = await fetch(`/api/v1/pipelines/${name}/run`, { method: 'POST' })
      if (!response.ok) {
        const body = await response.json().catch(() => ({ error: `HTTP ${response.status}` }))
        throw new Error(body.error ?? `HTTP ${response.status}`)
      }
      return response.json()
    }
    return window.go!.main.App.RunPipeline(name)
  }, [isWailsAvailable])

  const resetPipelineFailures = useCallback(async (name: string): Promise<boolean> => {
    if (!isWailsAvailable()) {
      const response = await fetch(`/api/v1/pipelines/${name}/failures`, { method: 'DELETE' })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      const data = await response.json()
      return data.resumed
    }
    return window.go!.main.App.ResetPipelineFailures(name)
  }, [isWailsAvailable])

  const getPipelineMemory = useCallback(async (name: string, limit: number = 50): Promise<MemoryEvent[]> => {
    if (!isWailsAvailable()) {
      const response = await fetch(
        `/api/v1/pipelines/${encodeURIComponent(name)}/memory?limit=${limit}`
      )
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      const body = await response.json()
      return Array.isArray(body.events) ? body.events : []
    }
    return window.go!.main.App.GetPipelineMemory(name, limit)
  }, [isWailsAvailable])

  return {
    isWailsAvailable: isWailsAvailable(),
    getDrafts,
    approveDraft,
    rejectDraft,
    listPipelines,
    runPipeline,
    resetPipelineFailures,
    getPipelineMemory,
  }
}