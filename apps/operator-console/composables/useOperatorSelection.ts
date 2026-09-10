import { computed, ref } from 'vue'

export type OperatorSelectionKind = 'none' | 'explicit' | 'page' | 'frozen_filter'

export interface OperatorSelectionTarget {
 id: string
 expectedVersion?: number
}

export type OperatorSelectionReconfirmationReason =
 | 'filter_changed'
 | 'context_changed'
 | 'grant_changed'
 | 'collection_changed'
 | 'expired'

export type OperatorSelection =
 | { kind: 'none'; domain: string; version: number }
 | { kind: 'explicit'; domain: string; version: number; targets: OperatorSelectionTarget[] }
 | { kind: 'page'; domain: string; version: number; cursor: string; targets: OperatorSelectionTarget[] }
 | {
  kind: 'frozen_filter'
  domain: string
  version: number
  selectionToken: string
  filterFingerprint: string
  targetCount: number
  excludedIds: string[]
  expiresAt: string
  reconfirmationRequired: boolean
  reconfirmationReason?: OperatorSelectionReconfirmationReason
 }

export interface OperatorSelectionKeyboardEvent {
 key: string
 preventDefault: () => void
}

const MAX_IDENTIFIER_BYTES = 256
const MAX_PAGE_CURSOR_BYTES = 512
const MAX_SELECTION_TARGETS = 1_000
const textEncoder = new TextEncoder()

function isText(value: unknown, limit = MAX_IDENTIFIER_BYTES): value is string {
 return typeof value === 'string'
  && value.length > 0
  && textEncoder.encode(value).byteLength <= limit
  && value.trim() === value
  && !/[\u0000-\u001f\u007f]/.test(value)
}

function isDomain(value: unknown): value is string {
 return isText(value, 64) && /^[a-z][a-z0-9_-]*$/.test(value)
}

function isFingerprint(value: unknown): value is string {
 return typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value)
}

function isVersion(value: unknown): value is number {
 return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isPositiveVersion(value: unknown): value is number {
 return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function isToken(value: unknown): value is string {
 return typeof value === 'string'
  && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value)
}

function isReason(value: unknown): value is OperatorSelectionReconfirmationReason {
 return value === 'filter_changed'
  || value === 'context_changed'
  || value === 'grant_changed'
  || value === 'collection_changed'
  || value === 'expired'
}

function readTargets(value: unknown, required: boolean): OperatorSelectionTarget[] {
 if (!Array.isArray(value)) {
  if (required) throw new TypeError('selection targets are required')
  return []
 }
 if (required && value.length === 0) throw new TypeError('selection targets are required')
 if (value.length > MAX_SELECTION_TARGETS) throw new TypeError('selection targets exceed the maximum')
 const ids = new Set<string>()
 const targets: OperatorSelectionTarget[] = []
 for (const entry of value) {
  if (entry === null || typeof entry !== 'object' || Array.isArray(entry)) throw new TypeError('selection target is invalid')
  const id = Reflect.get(entry, 'id')
  if (!isText(id) || ids.has(id)) throw new TypeError('selection target is ambiguous')
  const expectedVersion = Reflect.get(entry, 'expected_version')
  if (expectedVersion !== undefined && !isPositiveVersion(expectedVersion)) throw new TypeError('selection target version is invalid')
  ids.add(id)
  targets.push(expectedVersion === undefined ? { id } : { id, expectedVersion })
 }
 return targets
}

function readExcludedIDs(value: unknown): string[] {
 if (!Array.isArray(value)) throw new TypeError('selection exclusions are required')
 if (value.length > MAX_SELECTION_TARGETS) throw new TypeError('selection exclusions exceed the maximum')
 const ids = new Set<string>()
 for (const entry of value) {
  if (!isText(entry) || ids.has(entry)) throw new TypeError('selection exclusion is invalid')
  ids.add(entry)
 }
 return [...ids]
}

function readVersion(value: object): number {
 const version = Reflect.get(value, 'selection_version')
 if (version === undefined) return 0
 if (!isVersion(version)) throw new TypeError('selection version is invalid')
 return version
}

function readReconfirmation(value: object) {
 const required = Reflect.get(value, 'reconfirmation_required')
 if (typeof required !== 'boolean') throw new TypeError('selection reconfirmation state is invalid')
 const reason = Reflect.get(value, 'reconfirmation_reason')
 if (required && !isReason(reason)) throw new TypeError('selection reconfirmation reason is invalid')
 if (!required && reason !== undefined) throw new TypeError('unexpected selection reconfirmation reason')
 return { required, reason: isReason(reason) ? reason : undefined }
}

/** Parses the server-owned snapshot once at the browser boundary. */
export function parseOperatorSelectionSnapshot(value: unknown): OperatorSelection {
 if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new TypeError('selection snapshot is invalid')
 const domain = Reflect.get(value, 'domain')
 const kind = Reflect.get(value, 'kind')
 if (!isDomain(domain) || typeof kind !== 'string') throw new TypeError('selection snapshot identity is invalid')
 const version = readVersion(value)

 switch (kind) {
  case 'none':
   return { kind, domain, version }
  case 'explicit':
   return { kind, domain, version, targets: readTargets(Reflect.get(value, 'targets'), true) }
  case 'page': {
   const cursor = Reflect.get(value, 'cursor')
   if (!isText(cursor, MAX_PAGE_CURSOR_BYTES)) throw new TypeError('selection cursor is invalid')
   return { kind, domain, version, cursor, targets: readTargets(Reflect.get(value, 'targets'), true) }
  }
  case 'frozen_filter': {
   const selectionToken = Reflect.get(value, 'selection_token')
   const filterFingerprint = Reflect.get(value, 'filter_fingerprint')
   const targetCount = Reflect.get(value, 'target_count')
   const expiresAt = Reflect.get(value, 'expires_at')
   if (!isToken(selectionToken) || !isFingerprint(filterFingerprint) || !isVersion(targetCount) || !isText(expiresAt) || Number.isNaN(Date.parse(expiresAt))) {
    throw new TypeError('frozen selection snapshot is invalid')
   }
   const excludedIds = readExcludedIDs(Reflect.get(value, 'excluded_ids'))
   const reconfirmation = readReconfirmation(value)
   return {
    kind,
    domain,
    version,
    selectionToken,
    filterFingerprint,
    targetCount,
    excludedIds,
    expiresAt,
    reconfirmationRequired: reconfirmation.required,
    reconfirmationReason: reconfirmation.reason,
   }
  }
  default:
   throw new TypeError('selection kind is invalid')
 }
}

function cloneTargets(targets: OperatorSelectionTarget[]): OperatorSelectionTarget[] {
 return targets.map((target) => target.expectedVersion === undefined ? { id: target.id } : { id: target.id, expectedVersion: target.expectedVersion })
}

function validatedTargets(targets: OperatorSelectionTarget[]): OperatorSelectionTarget[] {
 return readTargets(targets.map((target) => target.expectedVersion === undefined
  ? { id: target.id }
  : { id: target.id, expected_version: target.expectedVersion }), true)
}

function none(domain: string): OperatorSelection {
 return { kind: 'none', domain, version: 0 }
}

function selectedVisibleIDs(selection: OperatorSelection): Set<string> {
 if (selection.kind !== 'explicit' && selection.kind !== 'page') return new Set()
 return new Set(selection.targets.map((target) => target.id))
}

/**
 * Holds client presentation state for one collection domain. It deliberately
 * exposes no authorization result: a frozen token remains intent for a later
 * domain action, not permission to perform one.
 */
export function createOperatorSelection(domain: string) {
 if (!isDomain(domain)) throw new TypeError('selection domain is invalid')
 const current = ref<OperatorSelection>(none(domain))

 const selectedCount = computed(() => current.value.kind === 'frozen_filter'
  ? current.value.targetCount
  : current.value.kind === 'none'
   ? 0
   : current.value.targets.length)

 function clear() {
  current.value = none(domain)
 }

 function selectExplicit(targets: OperatorSelectionTarget[]) {
  current.value = { kind: 'explicit', domain, version: 0, targets: validatedTargets(targets) }
 }

 function selectCurrentPage(cursor: string, targets: OperatorSelectionTarget[]) {
  if (!isText(cursor, MAX_PAGE_CURSOR_BYTES)) throw new TypeError('selection cursor is invalid')
  current.value = { kind: 'page', domain, version: 0, cursor, targets: validatedTargets(targets) }
 }

 function applySnapshot(snapshot: OperatorSelection) {
  if (snapshot.domain !== domain) throw new TypeError('selection snapshot belongs to another domain')
  current.value = snapshot.kind === 'frozen_filter'
   ? { ...snapshot, excludedIds: [...snapshot.excludedIds] }
   : snapshot.kind === 'none'
    ? { ...snapshot }
    : { ...snapshot, targets: cloneTargets(snapshot.targets) }
 }

 function invalidate(reason: OperatorSelectionReconfirmationReason) {
  if (current.value.kind !== 'frozen_filter') {
   clear()
   return
  }
  current.value = { ...current.value, reconfirmationRequired: true, reconfirmationReason: reason }
 }

 function headerAriaChecked(visibleTargets: OperatorSelectionTarget[]): 'false' | 'mixed' | 'true' {
  const visible = validatedTargets(visibleTargets)
  const selected = selectedVisibleIDs(current.value)
  const selectedVisible = visible.filter((target) => selected.has(target.id)).length
  if (selectedVisible === 0) return 'false'
  if (selectedVisible === visible.length) return 'true'
  return 'mixed'
 }

 function handleHeaderKeydown(event: OperatorSelectionKeyboardEvent, cursor: string, visibleTargets: OperatorSelectionTarget[]): boolean {
  if (event.key !== ' ' && event.key !== 'Enter') return false
  event.preventDefault()
  if (headerAriaChecked(visibleTargets) === 'true') clear()
  else selectCurrentPage(cursor, visibleTargets)
  return true
 }

 return {
  current,
  selectedCount,
  clear,
  selectExplicit,
  selectCurrentPage,
  applySnapshot,
  invalidate,
  headerAriaChecked,
  handleHeaderKeydown,
 }
}
