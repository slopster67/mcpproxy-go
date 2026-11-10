export * from './api'

export interface Toast {
  type: 'success' | 'error' | 'warning' | 'info'
  title: string
  message?: string
  link?: { url: string; label: string }
}
