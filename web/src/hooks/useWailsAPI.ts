// useWailsAPI.ts - Hook para comunicação com backend via Wails bindings

interface WailsRuntime {
  go: {
    main: {
      App: {
        GetVersion(): Promise<string>
        GetDrafts(): Promise<any[]>
        ApproveDraft(id: string): Promise<void>
        RejectDraft(id: string, reason: string): Promise<void>
      }
    }
  }
  EventsOn(eventName: string, callback: (data: any) => void): void
  EventsOff(eventName: string): void
}

declare global {
  interface Window {
    go?: WailsRuntime['go']
    runtime?: {
      EventsOn: WailsRuntime['EventsOn']
      EventsOff: WailsRuntime['EventsOff']
    }
  }
}

export function useWailsAPI() {
  const isWailsAvailable = () => {
    return typeof window !== 'undefined' && window.go?.main?.App
  }

  const getDrafts = async (): Promise<any[]> => {
    if (!isWailsAvailable()) {
      // Fallback para HTTP quando Wails não está disponível (modo dev browser)
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

  return {
    isWailsAvailable: isWailsAvailable(),
    getDrafts,
    approveDraft,
    rejectDraft
  }
}