// useWailsAPI.ts - Hook para comunicação com backend via Wails bindings

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

interface WailsRuntime {
  go: {
    main: {
      App: {
        GetVersion(): Promise<string>
        // Draft methods (increment 7b)
        GetDrafts(): Promise<any[]>
        ApproveDraft(id: string): Promise<void>
        RejectDraft(id: string, reason: string): Promise<void>
        // Pipeline methods (increment 7c)
        ListPipelines(): Promise<PipelineSummary[]>
        RunPipeline(name: string): Promise<PipelineResult>
        ResetPipelineFailures(name: string): Promise<boolean>
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
  const isWailsAvailable = () => {
    return typeof window !== 'undefined' && window.go?.main?.App
  }

  // --- Draft methods (increment 7b) ---

  const getDrafts = async (): Promise<any[]> => {
    if (!isWailsAvailable()) {
      const response = await fetch('/api/v1/drafts')
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      return response.json()
    }
    return window.go!.main.App.GetDrafts()
  }

  const approveDraft = async (id: string): Promise<void> => {
    if (!isWailsAvailable()) {
      const response = await fetch(`/api/v1/drafts/${id}/approve`, { method: 'POST' })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      return
    }
    return window.go!.main.App.ApproveDraft(id)
  }

  const rejectDraft = async (id: string, reason: string): Promise<void> => {
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
  }

  // --- Pipeline methods (increment 7c) ---

  const listPipelines = async (): Promise<PipelineSummary[]> => {
    if (!isWailsAvailable()) {
      const response = await fetch('/api/v1/pipelines')
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      return response.json()
    }
    return window.go!.main.App.ListPipelines()
  }

  const runPipeline = async (name: string): Promise<PipelineResult> => {
    if (!isWailsAvailable()) {
      const response = await fetch(`/api/v1/pipelines/${name}/run`, { method: 'POST' })
      if (!response.ok) {
        const body = await response.json().catch(() => ({ error: `HTTP ${response.status}` }))
        throw new Error(body.error ?? `HTTP ${response.status}`)
      }
      return response.json()
    }
    return window.go!.main.App.RunPipeline(name)
  }

  const resetPipelineFailures = async (name: string): Promise<boolean> => {
    if (!isWailsAvailable()) {
      const response = await fetch(`/api/v1/pipelines/${name}/failures`, { method: 'DELETE' })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      const data = await response.json()
      return data.resumed
    }
    return window.go!.main.App.ResetPipelineFailures(name)
  }

  return {
    isWailsAvailable: isWailsAvailable(),
    // Draft methods
    getDrafts,
    approveDraft,
    rejectDraft,
    // Pipeline methods
    listPipelines,
    runPipeline,
    resetPipelineFailures,
  }
}