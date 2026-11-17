import { useEffect } from 'react'

export interface ToastData {
  id: number
  type: 'success' | 'error' | 'info'
  message: string
}

interface ToastProps {
  toast: ToastData
  onDismiss: (id: number) => void
  durationMs?: number
}

export default function Toast({ toast, onDismiss, durationMs = 5000 }: ToastProps) {
  useEffect(() => {
    const timer = setTimeout(() => onDismiss(toast.id), durationMs)
    return () => clearTimeout(timer)
  }, [toast.id, durationMs, onDismiss])

  return (
    <div className={`toast toast--${toast.type}`} role="status">
      <span className="toast-icon">
        {toast.type === 'success' && '✓'}
        {toast.type === 'error' && '✗'}
        {toast.type === 'info' && 'ℹ'}
      </span>
      <span className="toast-message">{toast.message}</span>
      <button
        type="button"
        className="toast-close"
        onClick={() => onDismiss(toast.id)}
        aria-label="Dismiss"
      >
        ×
      </button>
    </div>
  )
}

export function ToastContainer({
  toasts,
  onDismiss,
}: {
  toasts: ToastData[]
  onDismiss: (id: number) => void
}) {
  return (
    <div className="toast-container">
      {toasts.map((t) => (
        <Toast key={t.id} toast={t} onDismiss={onDismiss} />
      ))}
    </div>
  )
}