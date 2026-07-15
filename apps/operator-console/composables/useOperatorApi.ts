export const OPERATOR_LOAD_KINDS = [
  'pending',
  'error',
  'empty',
  'gated',
  'stale',
  'mustbuild',
  'live',
] as const

export type OperatorLoadKind = typeof OPERATOR_LOAD_KINDS[number]

export interface OperatorEndpointEvidence {
  endpoint: string
  source: string
  status?: number
  flag?: string
  reason?: string
  retryable?: boolean
}

export const OPERATOR_DIAGNOSIS_CATEGORIES = [
  'unauthorized',
  'forbidden',
  'endpoint-missing',
  'request-rejected',
  'server-unavailable',
  'unreachable',
  'invalid-response',
] as const

/**
 * Typed diagnosis category — the transport seam classifies failures; the
 * presentation boundary maps the category through locale keys
 * (`errors.diagnosis.<category>`). The seam itself never returns operator prose.
 */
export type OperatorDiagnosisCategory = typeof OPERATOR_DIAGNOSIS_CATEGORIES[number]

export interface OperatorSourceError {
  message: string
  /**
   * Typed diagnosis set by the transport seam. Optional only for page-local
   * parse errors constructed outside this seam; the presentation boundary
   * falls back to `unreachable`.
   */
  category?: OperatorDiagnosisCategory
  source: string
  path: string
  method: string
  status?: number
  retryable: boolean
}

export interface OperatorRetry<T = unknown> {
  source: string
  run: () => Promise<OperatorLoadState<T>>
}

export type OperatorLoadState<T> =
  | { kind: 'pending'; evidence: OperatorEndpointEvidence; startedAt: string; data?: T }
  | { kind: 'error'; evidence: OperatorEndpointEvidence; error: OperatorSourceError; retry: OperatorRetry<T>; updatedAt: string; data?: T }
  | { kind: 'empty'; evidence: OperatorEndpointEvidence; updatedAt: string; data: T }
  | { kind: 'gated'; evidence: OperatorEndpointEvidence; flag: string; reason: string; updatedAt: string; data?: T }
  | { kind: 'stale'; evidence: OperatorEndpointEvidence; reason: string; updatedAt: string; data?: T }
  | { kind: 'mustbuild'; evidence: OperatorEndpointEvidence; reason: string; updatedAt: string; data?: T }
  | { kind: 'live'; evidence: OperatorEndpointEvidence; updatedAt: string; data: T }

export interface OperatorLoadOptions<T> extends RequestInit {
  source?: string
  evidence?: Partial<OperatorEndpointEvidence>
  empty?: (data: T) => boolean
  gated?: { flag: string; reason: string }
  stale?: { reason: string }
  mustbuild?: { reason: string }
}

export interface OperatorFetchOptions extends RequestInit {
  source?: string
}

export interface OperatorMutationOptions<T> {
  action: string
  evidence: OperatorEndpointEvidence
  snapshot?: () => T
  optimistic?: () => void | Promise<void>
  run: () => Promise<T>
  rollback?: (snapshot: T | undefined, error: OperatorSourceError) => void | Promise<void>
  refresh?: () => void | Promise<void>
}

export type OperatorMutationResult<T> =
  | { kind: 'success'; action: string; data: T; evidence: OperatorEndpointEvidence; refreshed: boolean }
  | { kind: 'rollback'; action: string; error: OperatorSourceError; evidence: OperatorEndpointEvidence; rolledBack: boolean; refresh?: () => void | Promise<void> }

export interface OperatorUnsupportedAction {
  kind: 'mustbuild'
  action: string
  operable: false
  evidence: OperatorEndpointEvidence
}

export const DEFAULT_OPERATOR_API_BASE = '/api'

function nowIso() {
  return new Date().toISOString()
}

function normalizeBase(base: string) {
  return base.replace(/\/+$/, '') || DEFAULT_OPERATOR_API_BASE
}

function normalizePath(path: string) {
  const clean = path.trim()
  return clean.startsWith('/') ? clean : `/${clean}`
}

function isRetryableStatus(status?: number) {
  return status === undefined || status === 0 || status === 408 || status === 429 || status >= 500
}

export function operatorApiBase(): string {
  const configured = useRuntimeConfig().public.apiBase as string | undefined
  return configured && configured.trim() ? normalizeBase(configured.trim()) : DEFAULT_OPERATOR_API_BASE
}

export function operatorApiUrl(path: string, base = operatorApiBase()): string {
  if (/^https?:\/\//i.test(path)) {
    return path
  }

  const cleanBase = normalizeBase(base)
  const cleanPath = normalizePath(path)

  if (cleanBase.endsWith('/api') && cleanPath === '/api') {
    return cleanBase
  }

  if (cleanBase.endsWith('/api') && cleanPath.startsWith('/api/')) {
    return `${cleanBase}${cleanPath.slice(4)}`
  }

  return `${cleanBase}${cleanPath}`
}

export function endpointEvidence(
  endpoint: string,
  source = 'operator-api',
  extra: Partial<OperatorEndpointEvidence> = {},
): OperatorEndpointEvidence {
  return {
    endpoint,
    source,
    ...extra,
  }
}

export class OperatorFetchError extends Error {
  readonly status?: number
  readonly category?: OperatorDiagnosisCategory
  readonly source: string
  readonly path: string
  readonly method: string
  readonly retryable: boolean

  constructor(message: string, meta: OperatorSourceError) {
    super(message)
    this.name = 'OperatorFetchError'
    this.status = meta.status
    this.category = meta.category
    this.source = meta.source
    this.path = meta.path
    this.method = meta.method
    this.retryable = meta.retryable
  }
}

export function toOperatorSourceError(
  error: unknown,
  fallback: Pick<OperatorSourceError, 'source' | 'path' | 'method'>,
): OperatorSourceError {
  if (error instanceof OperatorFetchError) {
    return {
      message: error.message,
      category: error.category,
      status: error.status,
      source: error.source,
      path: error.path,
      method: error.method,
      retryable: error.retryable,
    }
  }

  return {
    message: error instanceof Error ? error.message : String(error),
    category: 'unreachable',
    source: fallback.source,
    path: fallback.path,
    method: fallback.method,
    retryable: true,
  }
}

/**
 * Classify an HTTP failure into a typed diagnosis category.
 * No English operator prose leaves this seam — the presentation boundary
 * translates the category via `errors.diagnosis.<category>` locale keys.
 */
export function operatorErrorDiagnosis(status?: number): OperatorDiagnosisCategory {
  if (status === 401) return 'unauthorized'
  if (status === 403) return 'forbidden'
  if (status === 404) return 'endpoint-missing'
  if (status !== undefined && status >= 500) return 'server-unavailable'
  if (status !== undefined && status >= 400) return 'request-rejected'
  return 'unreachable'
}

/** Resolve the typed category for any source error, deriving from status when unset. */
export function operatorDiagnosisCategory(
  error: Pick<OperatorSourceError, 'category' | 'status'> | null | undefined,
): OperatorDiagnosisCategory {
  if (!error) return 'unreachable'
  return error.category ?? operatorErrorDiagnosis(error.status)
}

/** Locale key for the primary operator copy of a failure. Presentation boundary only. */
export function operatorDiagnosisKey(
  error: Pick<OperatorSourceError, 'category' | 'status'> | null | undefined,
): string {
  return `errors.diagnosis.${operatorDiagnosisCategory(error)}`
}

async function readOperatorResponseText(
  response: Response,
  path: string,
  source: string,
  method: string,
): Promise<string> {
  try {
    return await response.text()
  } catch (error) {
    const detail = error instanceof Error ? error.message : String(error)
    const message = `Failed to read response from ${path}: ${detail}`
    throw new OperatorFetchError(message, {
      message,
      category: 'invalid-response',
      status: response.status,
      source,
      path,
      method,
      retryable: true,
    })
  }
}

export async function operatorFetchJson<T>(
  path: string,
  init: RequestInit = {},
  source = 'operator-api',
): Promise<T> {
  const method = String(init.method || 'GET').toUpperCase()
  let response: Response

  try {
    response = await fetch(operatorApiUrl(path), {
      ...init,
      credentials: 'include',
    })
  } catch (error) {
    const mapped = toOperatorSourceError(error, { source, path, method })
    throw new OperatorFetchError(mapped.message, mapped)
  }

  const text = await readOperatorResponseText(response, path, source, method)

  if (!response.ok) {
    const category = operatorErrorDiagnosis(response.status)
    const message = `${method} ${path} failed with status ${response.status}`
    throw new OperatorFetchError(message, {
      message,
      category,
      status: response.status,
      source,
      path,
      method,
      retryable: isRetryableStatus(response.status),
    })
  }

  if (response.status === 204) {
    return undefined as T
  }

  if (!text.trim()) {
    return undefined as T
  }

  const contentType = response.headers.get('content-type') || ''
  if (!contentType.includes('application/json')) {
    return text as T
  }

  try {
    return JSON.parse(text) as T
  } catch (error) {
    const detail = error instanceof Error ? error.message : String(error)
    const message = `Invalid JSON from ${path}: ${detail}`
    throw new OperatorFetchError(message, {
      message,
      category: 'invalid-response',
      status: response.status,
      source,
      path,
      method,
      retryable: false,
    })
  }
}

export function pendingState<T>(evidence: OperatorEndpointEvidence, data?: T): OperatorLoadState<T> {
  return { kind: 'pending', evidence, startedAt: nowIso(), data }
}

export function errorState<T>(
  evidence: OperatorEndpointEvidence,
  error: OperatorSourceError,
  retry: OperatorRetry<T>,
  data?: T,
): OperatorLoadState<T> {
  return {
    kind: 'error',
    evidence: { ...evidence, status: error.status, retryable: error.retryable },
    error,
    retry,
    updatedAt: nowIso(),
    data,
  }
}

export function emptyState<T>(evidence: OperatorEndpointEvidence, data: T): OperatorLoadState<T> {
  return { kind: 'empty', evidence, updatedAt: nowIso(), data }
}

export function gatedState<T>(
  evidence: OperatorEndpointEvidence,
  flag: string,
  reason: string,
  data?: T,
): OperatorLoadState<T> {
  return { kind: 'gated', evidence: { ...evidence, flag, reason }, flag, reason, updatedAt: nowIso(), data }
}

export function staleState<T>(evidence: OperatorEndpointEvidence, reason: string, data?: T): OperatorLoadState<T> {
  return { kind: 'stale', evidence: { ...evidence, reason }, reason, updatedAt: nowIso(), data }
}

export function mustBuildState<T>(evidence: OperatorEndpointEvidence, reason: string, data?: T): OperatorLoadState<T> {
  return { kind: 'mustbuild', evidence: { ...evidence, reason }, reason, updatedAt: nowIso(), data }
}

export function liveState<T>(evidence: OperatorEndpointEvidence, data: T): OperatorLoadState<T> {
  return { kind: 'live', evidence, updatedAt: nowIso(), data }
}

export async function loadOperatorJson<T>(
  path: string,
  options: OperatorLoadOptions<T> = {},
): Promise<OperatorLoadState<T>> {
  const {
    source = 'operator-api',
    evidence: evidenceInput,
    empty,
    gated,
    stale,
    mustbuild,
    ...init
  } = options
  const evidence = endpointEvidence(path, source, evidenceInput)

  if (gated) {
    return gatedState<T>(evidence, gated.flag, gated.reason)
  }

  if (stale) {
    return staleState<T>(evidence, stale.reason)
  }

  if (mustbuild) {
    return mustBuildState<T>(evidence, mustbuild.reason)
  }

  try {
    const data = await operatorFetchJson<T>(path, init, source)
    if (empty?.(data)) {
      return emptyState(evidence, data)
    }
    return liveState(evidence, data)
  } catch (error) {
    const mapped = toOperatorSourceError(error, {
      source,
      path,
      method: String(init.method || 'GET').toUpperCase(),
    })
    return errorState(evidence, mapped, {
      source,
      run: () => loadOperatorJson(path, options),
    })
  }
}

export async function runOperatorMutation<T>(options: OperatorMutationOptions<T>): Promise<OperatorMutationResult<T>> {
  const snapshot = options.snapshot?.()

  try {
    await options.optimistic?.()
    const data = await options.run()
    await options.refresh?.()
    return {
      kind: 'success',
      action: options.action,
      data,
      evidence: options.evidence,
      refreshed: Boolean(options.refresh),
    }
  } catch (error) {
    const mapped = toOperatorSourceError(error, {
      source: options.evidence.source,
      path: options.evidence.endpoint,
      method: 'MUTATION',
    })
    await options.rollback?.(snapshot, mapped)
    return {
      kind: 'rollback',
      action: options.action,
      error: mapped,
      evidence: { ...options.evidence, status: mapped.status, retryable: mapped.retryable },
      rolledBack: Boolean(options.rollback),
      refresh: options.refresh,
    }
  }
}

export function unsupportedOperatorAction(
  action: string,
  endpoint: string,
  reason: string,
): OperatorUnsupportedAction {
  return {
    kind: 'mustbuild',
    action,
    operable: false,
    evidence: endpointEvidence(endpoint, 'mustbuild', { reason }),
  }
}
