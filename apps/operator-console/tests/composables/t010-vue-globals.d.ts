import type { ComputedRef } from 'vue'

declare global {
  function computed<T>(getter: () => T): ComputedRef<T>
  function onBeforeUnmount(hook: () => void): void
  function onMounted(hook: () => void): void
  function useRoute(): { query: Record<string, string | string[] | undefined> }
}

export { }
