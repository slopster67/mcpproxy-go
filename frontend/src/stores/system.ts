import { defineStore } from 'pinia'
import { toast as notify } from 'vue-sonner'
import { ref } from 'vue'
import type { ChangeFrame, Toast } from '@/types'
import api from '@/services/api'
import { dispatchChange, dispatchHello, streamStarting, streamStopped } from '@/services/events'

const RETRY_MIN_MS = 1000
const RETRY_MAX_MS = 30_000
// A brief reconnect stays quiet; the banner shows only when it persists.
const LOST_AFTER_MS = 2000

export const useSystemStore = defineStore('system', () => {
  const connected = ref(false)
  const connectionLost = ref(false)

  // While the dashboard session is gone the sign-in redirect owns the screen;
  // the connection banner and page errors stay quiet.
  const authRequired = ref(false)

  function setAuthRequired(value: boolean) {
    authRequired.value = value
    if (value) {
      clearLostTimer()
      connectionLost.value = false
    }
  }

  // One EventSource at a time. `generation` retires callbacks of a closed
  // source, which browsers may still deliver.
  let source: EventSource | null = null
  let generation = 0
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let lostTimer: ReturnType<typeof setTimeout> | null = null
  let retryDelay = RETRY_MIN_MS
  // Re-checks the dashboard session when the stream is refused.
  let onStreamError: () => void = () => {}

  function clearLostTimer() {
    if (lostTimer) clearTimeout(lostTimer)
    lostTimer = null
  }
  function clearRetryTimer() {
    if (retryTimer) clearTimeout(retryTimer)
    retryTimer = null
  }

  function connectEventSource(onError?: () => void) {
    if (onError) onStreamError = onError
    clearRetryTimer()
    source?.close()
    const mine = ++generation
    streamStarting()
    const es = new EventSource(api.events.url)
    source = es
    const current = () => mine === generation

    es.addEventListener('hello', () => {
      if (!current()) return
      retryDelay = RETRY_MIN_MS
      clearLostTimer()
      connected.value = true
      connectionLost.value = false
      dispatchHello()
    })

    es.addEventListener('change', (event) => {
      if (!current()) return
      let frame: ChangeFrame
      try {
        frame = JSON.parse((event as MessageEvent).data) as ChangeFrame
      } catch (error) {
        console.error('Failed to parse change frame:', error)
        return
      }
      dispatchChange(frame)
    })

    // The browser's own retry cannot back off and gives up on a non-200
    // answer, so every error closes the source and reconnects here.
    es.onerror = () => {
      if (!current()) return
      // CLOSED: the stream was refused (for example an expired session), not
      // merely dropped.
      const refused = es.readyState === EventSource.CLOSED
      es.close()
      connected.value = false
      if (!connectionLost.value && !lostTimer && !authRequired.value) {
        lostTimer = setTimeout(() => {
          lostTimer = null
          if (current() && !connected.value && !authRequired.value) connectionLost.value = true
        }, LOST_AFTER_MS)
      }
      if (refused) onStreamError()
      const delay = retryDelay
      retryDelay = Math.min(retryDelay * 2, RETRY_MAX_MS)
      retryTimer = setTimeout(() => {
        retryTimer = null
        if (current()) connectEventSource()
      }, delay)
    }
  }

  function disconnectEventSource() {
    generation++
    streamStopped()
    clearRetryTimer()
    clearLostTimer()
    source?.close()
    source = null
    retryDelay = RETRY_MIN_MS
    connected.value = false
    connectionLost.value = false
  }

  function addToast(toast: Toast): string | number {
    const link = toast.link
    return notify[toast.type](toast.title, {
      description: toast.message || undefined,
      action: link ? {
        label: link.label,
        onClick: () => { window.open(link.url, '_blank', 'noopener,noreferrer') },
      } : undefined,
    })
  }

  return {
    connected,
    connectionLost,
    authRequired,
    connectEventSource,
    disconnectEventSource,
    addToast,
    setAuthRequired,
  }
})
