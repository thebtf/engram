import type { ComputedRef, Ref } from 'vue'

declare global {
  interface ImportMeta {
    client: boolean
    dev: boolean
  }

  function computed<T>(getter: () => T): ComputedRef<T>
  function onBeforeUnmount(hook: () => void): void
  function onMounted(hook: () => void): void
  function useRoute(): { query: Record<string, string | string[] | undefined> }
  function useRuntimeConfig(): { public: Record<string, unknown> }
  function useState<T>(key: string, init: () => T): Ref<T>
}

export { }
