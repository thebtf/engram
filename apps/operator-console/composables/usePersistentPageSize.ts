import { onMounted, ref, watch } from 'vue'

export type OperatorPageSize = 10 | 25 | 50 | 0

export const OPERATOR_PAGE_SIZE_OPTIONS: OperatorPageSize[] = [10, 25, 50, 0]

const PAGE_SIZE_STORAGE_KEY = 'engram.console.pageSizes'

function isOperatorPageSize(value: unknown): value is OperatorPageSize {
  return typeof value === 'number' && OPERATOR_PAGE_SIZE_OPTIONS.includes(value as OperatorPageSize)
}

function readPageSizes(): Record<string, unknown> {
  if (!import.meta.client) return {}

  try {
    const raw = window.localStorage.getItem(PAGE_SIZE_STORAGE_KEY)
    if (!raw) return {}
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function writePageSize(key: string, size: OperatorPageSize) {
  if (!import.meta.client) return

  try {
    const saved = readPageSizes()
    saved[key] = size
    window.localStorage.setItem(PAGE_SIZE_STORAGE_KEY, JSON.stringify(saved))
  } catch {
    // Private mode, quota, or locked storage should not break the live console.
  }
}

export function resolvePageSize(size: OperatorPageSize, total: number) {
  return size === 0 ? Math.max(total, 1) : size
}

export function usePersistentPageSize(key: string, initial: OperatorPageSize = 10) {
  const fallback = isOperatorPageSize(initial) ? initial : 10
  const pageSize = ref<OperatorPageSize>(fallback)
  const hydrated = ref(false)

  onMounted(() => {
    const saved = readPageSizes()[key]
    if (isOperatorPageSize(saved)) {
      pageSize.value = saved
    }
    hydrated.value = true
  })

  watch(pageSize, (size) => {
    if (!isOperatorPageSize(size)) {
      pageSize.value = fallback
      return
    }
    if (hydrated.value) {
      writePageSize(key, size)
    }
  })

  return {
    pageSize,
    pageSizeOptions: OPERATOR_PAGE_SIZE_OPTIONS,
  }
}
