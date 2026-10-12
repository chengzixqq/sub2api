import { onBeforeUnmount, onMounted, watch, type Ref } from 'vue'

export function monitorRefreshMilliseconds(seconds: unknown): number {
  return typeof seconds === 'number' && Number.isInteger(seconds) && seconds >= 30 && seconds <= 300 ? seconds * 1000 : 60_000
}
export function useMonitorPolling(refresh: () => unknown, seconds: Ref<number | undefined>, enabled: Ref<boolean>, loading: Ref<boolean>) {
  let timer: ReturnType<typeof setInterval> | undefined
  let mounted = false
  function resume() {
    if (enabled.value && document.visibilityState === 'visible' && !loading.value) void refresh()
  }
  function schedule() {
    clearInterval(timer)
    if (mounted && enabled.value) timer = setInterval(resume, monitorRefreshMilliseconds(seconds.value))
  }
  watch([seconds, enabled], schedule)
  onMounted(() => { mounted = true; schedule(); document.addEventListener('visibilitychange', resume) })
  onBeforeUnmount(() => { mounted = false; clearInterval(timer); document.removeEventListener('visibilitychange', resume) })
}
