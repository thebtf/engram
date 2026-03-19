import { ref, computed, onUnmounted, type Ref } from 'vue'

interface PaginatedResponse<T> {
  items: T[]
  total: number
}

interface UsePaginationOptions<T> {
  /** Items per page (default 20) */
  pageSize?: number
  /** Fetch function that receives limit, offset, and abort signal */
  fetchFn: (limit: number, offset: number, signal: AbortSignal) => Promise<PaginatedResponse<T>>
}

export function usePagination<T>(options: UsePaginationOptions<T>) {
  const pageSize = options.pageSize ?? 20

  const items: Ref<T[]> = ref([])
  const total = ref(0)
  const offset = ref(0)
  const loading = ref(false)
  const error = ref<string | null>(null)

  let abortController: AbortController | null = null

  const currentPage = computed(() => Math.floor(offset.value / pageSize) + 1)
  const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))
  const hasMore = computed(() => offset.value + items.value.length < total.value)

  async function fetchPage() {
    abortController?.abort()
    abortController = new AbortController()

    loading.value = true
    error.value = null

    try {
      const result = await options.fetchFn(pageSize, offset.value, abortController.signal)
      items.value = result.items
      total.value = result.total
    } catch (err) {
      if (err instanceof Error && err.name === 'AbortError') return
      error.value = err instanceof Error ? err.message : 'Failed to load data'
      console.error('[usePagination] Fetch error:', err)
    } finally {
      loading.value = false
    }
  }

  function goToPage(page: number) {
    const newOffset = (page - 1) * pageSize
    if (newOffset >= 0 && newOffset !== offset.value) {
      offset.value = newOffset
      fetchPage()
    }
  }

  function setOffset(newOffset: number) {
    offset.value = newOffset
    fetchPage()
  }

  function reset() {
    offset.value = 0
    fetchPage()
  }

  onUnmounted(() => {
    abortController?.abort()
  })

  return {
    items,
    total,
    offset,
    loading,
    error,
    currentPage,
    totalPages,
    hasMore,
    pageSize,
    fetchPage,
    goToPage,
    setOffset,
    reset,
  }
}
