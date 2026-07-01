/**
 * Honesty classification — the load-bearing invariant of the operator console.
 * Every surface is one of: live | dormant (flag-gated) | stale (tombstone) | mustbuild.
 * This composable is the single source of truth for the label + colour + rule so a
 * surface can never silently render a dead control as operable.
 *
 * Rules baked in (from .od/DESIGN.md §2 Named Rules):
 *  - stale  → outline only, never filled, never operable.
 *  - mustbuild → ALWAYS carries evidence (endpoint or gate flag) next to it.
 *  - dormant → flag-gated; show the gate flag as evidence.
 */
export type HonestyClass = 'live' | 'dormant' | 'stale' | 'mustbuild'

export interface HonestyMeta {
  cls: HonestyClass
  /** CSS var name for the colour, e.g. var(--class-live) */
  color: string
  /** Russian operator-facing label */
  label: string
  /** does this class REQUIRE an evidence string (endpoint/flag) shown beside it? */
  needsEvidence: boolean
  /** is the surface allowed to expose an operable control? stale never is. */
  operable: boolean
}

const TABLE: Record<HonestyClass, Omit<HonestyMeta, 'cls'>> = {
  live:      { color: 'var(--class-live)',      label: 'работает',       needsEvidence: false, operable: true },
  dormant:   { color: 'var(--class-dormant)',   label: 'за флагом',      needsEvidence: true,  operable: true },
  stale:     { color: 'var(--class-stale)',     label: 'надгробие',      needsEvidence: false, operable: false },
  mustbuild: { color: 'var(--class-mustbuild)', label: 'нужно построить', needsEvidence: true, operable: false },
}

export function useHonesty(cls: HonestyClass): HonestyMeta {
  return { cls, ...TABLE[cls] }
}

/** Guard for dev: throws in non-prod if a mustbuild/dormant surface ships without evidence. */
export function assertEvidence(cls: HonestyClass, evidence?: string) {
  const meta = TABLE[cls]
  if (meta.needsEvidence && !evidence && import.meta.dev) {
    console.warn(`[honesty] "${cls}" surface is missing required evidence (endpoint/flag).`)
  }
}
