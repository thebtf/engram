import { onMounted, ref, watch } from 'vue'

export type OperatorPageSize = 10 | 25 | 50 | 'all'

export const OPERATOR_PAGE_SIZE_OPTIONS: OperatorPageSize[] = [10, 25, 50, 'all']

export const MEMORY_PAGE_SIZE_STORAGE_KEY = 'engram.operatorConsole.memory.pageSize'

function normalizePageSize(value: unknown): OperatorPageSize | null {
  if (value === 'all') return 'all'
  if (value === 10 || value === '10') return 10
  if (value === 25 || value === '25') return 25
  if (value === 50 || value === '50') return 50
  return null
}

function readPageSize(key: string): OperatorPageSize | null {
  if (!import.meta.client) return null

  try {
    return normalizePageSize(window.localStorage.getItem(key))
  } catch {
    return null
  }
}

function writePageSize(key: string, size: OperatorPageSize) {
  if (!import.meta.client) return

  try {
    window.localStorage.setItem(key, String(size))
  } catch {
    // Private mode, quota, or locked storage should not break the live console.
  }
}

export function resolvePageSize(size: OperatorPageSize, total: number) {
  return size === 'all' ? Math.max(total, 1) : size
}

export function usePersistentPageSize(key: string, initial: OperatorPageSize = 10) {
  const fallback = normalizePageSize(initial) || 10
  const pageSize = ref<OperatorPageSize>(fallback)
  const hydrated = ref(false)

  onMounted(() => {
    const saved = readPageSize(key)
    if (saved !== null) {
      pageSize.value = saved
    }
    hydrated.value = true
  })

  watch(pageSize, (size) => {
    const normalized = normalizePageSize(size)
    if (normalized === null) {
      pageSize.value = fallback
      return
    }
    if (hydrated.value) {
      writePageSize(key, normalized)
    }
  })

  return {
    pageSize,
    pageSizeOptions: OPERATOR_PAGE_SIZE_OPTIONS,
  }
}
