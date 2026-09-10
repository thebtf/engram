export const MUTATION_RESULT_KINDS = [
 'committed_verified',
 'committed_verification_pending',
 'partial',
 'outcome_unknown',
 'denied',
 'conflict',
 'validation_error',
 'stale',
 'unsupported',
 'offline',
 'timeout',
 'network',
 'failed',
] as const

export type MutationResultKind = typeof MUTATION_RESULT_KINDS[number]

export interface MutationRequest<TIntent = unknown> {
 requestId: string
 action: string
 intent: TIntent
}
export type MutationCurrentStateParser<TCurrent> = (value: unknown) => TCurrent | undefined


export type MutationVersion = string | number | null

export type MutationReadback<TCurrent> =
 | {
  kind: 'current'
  current: TCurrent
  version?: MutationVersion
  observedAt?: string
 }
 | {
  kind: 'authorized_absence'
  observedAt?: string
 }
 | {
  kind: 'non_disclosing'
  operationStatus: string
  observedAt?: string
 }

export const MUTATION_ITEM_OUTCOMES = [
 'committed',
 'denied',
 'conflict',
 'validation_error',
 'failed',
 'outcome_unknown',
] as const

export type MutationItemOutcome = typeof MUTATION_ITEM_OUTCOMES[number]

export interface MutationItemResult {
 targetId: string | number
 outcome: MutationItemOutcome
 observedVersion?: MutationVersion
}

type MutationBase<TIntent> = {
 request: MutationRequest<TIntent>
 httpStatus?: number
 operationId?: string
 code?: string
}

export type MutationFailureKind = Exclude<
 MutationResultKind,
 'committed_verified' | 'committed_verification_pending' | 'partial' | 'outcome_unknown'
>

export type MutationFailureResult<TIntent = unknown> = MutationBase<TIntent> & {
 kind: MutationFailureKind
}

export type MutationOutcomeUnknownResult<TIntent = unknown> = MutationBase<TIntent> & {
 kind: 'outcome_unknown'
 retry: 'manual'
}

export type MutationTransportEvidence =
 | { stage: 'pre_dispatch'; failure: 'offline' | 'network' }
 | { stage: 'dispatched' | 'response_received' }

const PRE_DISPATCH_NETWORK_EVIDENCE: MutationTransportEvidence = {
 stage: 'pre_dispatch',
 failure: 'network',
}
const DISPATCHED_TRANSPORT_EVIDENCE: MutationTransportEvidence = { stage: 'dispatched' }

export type MutationResult<TIntent = unknown, TCurrent = unknown> =
 | (MutationBase<TIntent> & {
  kind: 'committed_verified'
  httpStatus: number
  readback: MutationReadback<TCurrent>
 })
 | (MutationBase<TIntent> & {
  kind: 'committed_verification_pending'
  httpStatus: number
  commitment: 'accepted' | 'committed'
  reason: 'accepted' | 'readback_missing' | 'readback_invalid'
 })
 | (MutationBase<TIntent> & {
  kind: 'partial'
  httpStatus: number
  items: MutationItemResult[]
 })
 | MutationOutcomeUnknownResult<TIntent>
 | MutationFailureResult<TIntent>

interface MutationResponseBody {
 request_id?: unknown
 operation_id?: unknown
 operation_state?: unknown
 readback?: unknown
 item_results?: unknown
 code?: unknown
}

type MutationResponseBodyRead =
 | { kind: 'body'; body: MutationResponseBody }
 | { kind: 'empty' | 'invalid' | 'lost' }

interface MutationReadbackBody {
 authoritative?: unknown
 kind?: unknown
 current_state?: unknown
 current_version?: unknown
 observed_at?: unknown
 operation_status?: unknown
}

interface MutationItemResultBody {
 target_id?: unknown
 outcome?: unknown
 observed_version?: unknown
}

function parseReadback<TCurrent>(
 value: unknown,
 parseCurrentState: MutationCurrentStateParser<TCurrent>,
): MutationReadback<TCurrent> | undefined {
 if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined

 const readback = value as MutationReadbackBody
 if (readback.authoritative !== true) return undefined

 const observedAt = typeof readback.observed_at === 'string' && readback.observed_at.length > 0
  ? readback.observed_at
  : undefined

 switch (readback.kind) {
  case 'current': {
   if (!Object.hasOwn(readback, 'current_state')) return undefined
   const current = parseCurrentState(readback.current_state)
   if (current === undefined) return undefined
   const version = typeof readback.current_version === 'string' || typeof readback.current_version === 'number' || readback.current_version === null
    ? readback.current_version
    : undefined
   return {
    kind: 'current',
    current,
    ...(version === undefined ? {} : { version }),
    ...(observedAt === undefined ? {} : { observedAt }),
   }
  }
  case 'authorized_absence':
   return {
    kind: 'authorized_absence',
    ...(observedAt === undefined ? {} : { observedAt }),
   }
  case 'non_disclosing': {
   const operationStatus = typeof readback.operation_status === 'string' && readback.operation_status.length > 0
    ? readback.operation_status
    : undefined
   if (!operationStatus) return undefined
   return {
    kind: 'non_disclosing',
    operationStatus,
    ...(observedAt === undefined ? {} : { observedAt }),
   }
  }
  default:
   return undefined
 }
}

function parseItemResults(value: unknown): MutationItemResult[] | undefined {
 if (!Array.isArray(value)) return undefined

 const items: MutationItemResult[] = []
 for (const item of value) {
  if (typeof item !== 'object' || item === null || Array.isArray(item)) return undefined

  const itemResult = item as MutationItemResultBody
  const targetId = itemResult.target_id
  const outcome = itemResult.outcome
  if ((typeof targetId !== 'string' && typeof targetId !== 'number') || !MUTATION_ITEM_OUTCOMES.includes(outcome as MutationItemOutcome)) {
   return undefined
  }

  const observedVersion = typeof itemResult.observed_version === 'string' || typeof itemResult.observed_version === 'number' || itemResult.observed_version === null
   ? itemResult.observed_version
   : undefined
  items.push({
   targetId,
   outcome: outcome as MutationItemOutcome,
   ...(observedVersion === undefined ? {} : { observedVersion }),
  })
 }

 return items
}

function failureKindForStatus(status: number): Extract<MutationResultKind, 'denied' | 'conflict' | 'validation_error' | 'stale' | 'unsupported' | 'offline' | 'timeout' | 'failed'> {
 if (status === 401 || status === 403) return 'denied'
 if (status === 409) return 'conflict'
 if (status === 400 || status === 422) return 'validation_error'
 if (status === 410 || status === 412) return 'stale'
 if (status === 501) return 'unsupported'
 if (status === 503) return 'offline'
 if (status === 408 || status === 504) return 'timeout'
 return 'failed'
}

function failureResult<TIntent>(
 request: MutationRequest<TIntent>,
 kind: MutationFailureKind,
 metadata: Pick<MutationBase<TIntent>, 'httpStatus' | 'operationId' | 'code'> = {},
): MutationFailureResult<TIntent> {
 return { kind, request, ...metadata }
}


async function readResponseBody(response: Response): Promise<MutationResponseBodyRead> {
 let text: string
 try {
  text = await response.text()
 } catch {
  return { kind: 'lost' }
 }
 if (!text.trim()) return { kind: 'empty' }

 try {
  const body: unknown = JSON.parse(text)
  return typeof body === 'object' && body !== null && !Array.isArray(body)
   ? { kind: 'body', body: body as MutationResponseBody }
   : { kind: 'invalid' }
 } catch {
  return { kind: 'invalid' }
 }
}

/**
 * Maps one HTTP mutation response into durable client truth. A successful HTTP
 * response alone is never verified completion: only a completed operation with
 * an authoritative readback can produce `committed_verified`. A dispatched
 * transport failure is not proof of non-commit and requires manual reconciliation.
 */
export async function parseMutationResponse<TIntent, TCurrent>(
 request: MutationRequest<TIntent>,
 response: Response,
 parseCurrentState: MutationCurrentStateParser<TCurrent>,
): Promise<MutationResult<TIntent, TCurrent>> {
 const bodyRead = await readResponseBody(response)
 const body = bodyRead.kind === 'body' ? bodyRead.body : undefined
 const parsedOperationId = body && typeof body.operation_id === 'string' && body.operation_id.length > 0
  ? body.operation_id
  : undefined
 const parsedCode = body && typeof body.code === 'string' && body.code.length > 0
  ? body.code
  : undefined
 const metadata = {
  httpStatus: response.status,
  ...(parsedOperationId === undefined ? {} : { operationId: parsedOperationId }),
  ...(parsedCode === undefined ? {} : { code: parsedCode }),
 }
 const responseRequestId = body && typeof body.request_id === 'string' && body.request_id.length > 0
  ? body.request_id
  : undefined

 if (responseRequestId !== undefined && responseRequestId !== request.requestId) {
  return failureResult(request, 'failed', { ...metadata, code: 'request_reference_mismatch' })
 }

 if (response.status === 202) {
  return {
   kind: 'committed_verification_pending',
   request,
   ...metadata,
   commitment: 'accepted',
   reason: 'accepted',
  }
 }

 if (!response.ok) {
  return failureResult(request, failureKindForStatus(response.status), metadata)
 }

 if (response.status === 204) {
  return {
   kind: 'committed_verification_pending',
   request,
   ...metadata,
   commitment: 'committed',
   reason: 'readback_missing',
  }
 }

 if (bodyRead.kind === 'invalid' || bodyRead.kind === 'lost') {
  return { kind: 'outcome_unknown', request, retry: 'manual', ...metadata }
 }

 const state = body && typeof body.operation_state === 'string' && body.operation_state.length > 0
  ? body.operation_state
  : undefined
 if (!state) {
  return {
   kind: 'committed_verification_pending',
   request,
   ...metadata,
   commitment: 'committed',
   reason: 'readback_missing',
  }
 }

 if (response.status === 207 || state === 'partial') {
  const items = parseItemResults(body?.item_results)
  return items === undefined
   ? failureResult(request, 'failed', { ...metadata, code: 'invalid_partial_response' })
   : { kind: 'partial', request, ...metadata, items }
 }

 if (state === 'accepted' || state === 'queued' || state === 'pending' || state === 'running') {
  return {
   kind: 'committed_verification_pending',
   request,
   ...metadata,
   commitment: 'accepted',
   reason: 'accepted',
  }
 }

 if (state === 'committed') {
  return {
   kind: 'committed_verification_pending',
   request,
   ...metadata,
   commitment: 'committed',
   reason: 'readback_missing',
  }
 }

 if (state === 'completed') {
  const readback = parseReadback(body?.readback, parseCurrentState)
  return readback === undefined
   ? {
    kind: 'committed_verification_pending',
    request,
    ...metadata,
    commitment: 'committed',
    reason: body && Object.hasOwn(body, 'readback') ? 'readback_invalid' : 'readback_missing',
   }
   : { kind: 'committed_verified', request, ...metadata, readback }
 }

 return failureResult(request, 'failed', { ...metadata, code: 'invalid_mutation_state' })
}

export async function executeMutation<TIntent, TCurrent>(
 request: MutationRequest<TIntent>,
 response: Promise<Response>,
 parseCurrentState: MutationCurrentStateParser<TCurrent>,
): Promise<MutationResult<TIntent, TCurrent>> {
 let receivedResponse: Response
 try {
  receivedResponse = await response
 } catch (error) {
  return parseMutationTransportFailure(request, error, DISPATCHED_TRANSPORT_EVIDENCE)
 }
 return parseMutationResponse<TIntent, TCurrent>(request, receivedResponse, parseCurrentState)
}

export function parseMutationTransportFailure<TIntent>(
 request: MutationRequest<TIntent>,
 error: unknown,
 evidence: MutationTransportEvidence = PRE_DISPATCH_NETWORK_EVIDENCE,
): MutationFailureResult<TIntent> | MutationOutcomeUnknownResult<TIntent> {
 const name = error instanceof Error ? error.name : ''
 if (name === 'AbortError' || name === 'TimeoutError') return failureResult(request, 'timeout')
 if (evidence.stage === 'pre_dispatch') return failureResult(request, evidence.failure)
 return { kind: 'outcome_unknown', request, retry: 'manual' }
}
