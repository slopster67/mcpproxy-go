import { onScopeDispose, readonly, ref, watch } from 'vue'

/** Skip skeleton flashes on fast loads without delaying finished content. */
export function useDelayedLoading(isLoading: () => boolean, delayMs = 150) {
  const visible = ref(false)
  let timer: ReturnType<typeof setTimeout> | undefined
  const cancel = () => {
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
  }
  watch(
    isLoading,
    (loading) => {
      cancel()
      visible.value = false
      if (loading)
        timer = setTimeout(() => {
          timer = undefined
          visible.value = true
        }, delayMs)
    },
    { immediate: true }
  )
  onScopeDispose(cancel)
  return readonly(visible)
}
