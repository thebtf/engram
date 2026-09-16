import { expect, test } from '@playwright/test'
import {
  createOperatorSelection,
  parseOperatorSelectionSnapshot,
  type OperatorSelectionTarget,
} from '../../composables/useOperatorSelection'

const pageTargets: OperatorSelectionTarget[] = [
  { id: 'rule-1', expectedVersion: 3 },
  { id: 'rule-2', expectedVersion: 4 },
]

const digest = (character: string) => `sha256:${character.repeat(64)}`

test('collection selection keeps none, explicit IDs, visible page, and frozen filter semantically distinct', async ({ page }) => {
  const selection = createOperatorSelection('rules')
  expect(selection.current.value.kind).toBe('none')

  selection.selectExplicit([pageTargets[0]])
  expect(selection.current.value).toMatchObject({ kind: 'explicit', targets: [pageTargets[0]] })

  selection.selectCurrentPage('opaque-page-cursor', pageTargets)
  expect(selection.current.value).toMatchObject({ kind: 'page', cursor: 'opaque-page-cursor', targets: pageTargets })

  selection.applySnapshot(parseOperatorSelectionSnapshot({
    domain: 'rules',
    kind: 'frozen_filter',
    selection_version: 9,
    selection_token: '4e0add48-42c9-4f4f-b5c7-f89ebf2a6ee6',
    filter_fingerprint: digest('f'),
    target_count: 3,
    excluded_ids: ['rule-2'],
    expires_at: '2026-09-10T12:00:00.000Z',
    reconfirmation_required: false,
  }))
  expect(selection.current.value).toMatchObject({
    kind: 'frozen_filter',
    excludedIds: ['rule-2'],
    targetCount: 3,
  })

  selection.invalidate('collection_changed')
  expect(selection.current.value).toMatchObject({ kind: 'frozen_filter', reconfirmationRequired: true, reconfirmationReason: 'collection_changed' })

  await page.setContent('<label for="selection-header">Select current page</label><input id="selection-header" type="checkbox">')
  const header = page.locator('#selection-header')

  selection.selectExplicit([pageTargets[0]])
  await header.evaluate((element, ariaChecked) => {
    const input = element as HTMLInputElement
    input.checked = ariaChecked === 'true'
    input.indeterminate = ariaChecked === 'mixed'
    input.setAttribute('aria-checked', ariaChecked)
  }, selection.headerAriaChecked(pageTargets))
  await expect(header).toHaveAttribute('aria-checked', 'mixed')
  await expect(header).toHaveJSProperty('indeterminate', true)

  let prevented = false
  expect(selection.handleHeaderKeydown({ key: ' ', preventDefault: () => { prevented = true } }, 'opaque-page-cursor', pageTargets)).toBe(true)
  expect(prevented).toBe(true)
  expect(selection.current.value).toMatchObject({ kind: 'page', cursor: 'opaque-page-cursor', targets: pageTargets })
})

test('collection selection rejects over-bounds while retaining a fully excluded frozen snapshot', () => {
  expect(() => parseOperatorSelectionSnapshot({
    domain: 'rules',
    kind: 'explicit',
    targets: Array.from({ length: 1_001 }, (_, index) => ({ id: `rule-${index}` })),
  })).toThrow('selection targets exceed the maximum')

  expect(() => parseOperatorSelectionSnapshot({
    domain: 'rules',
    kind: 'page',
    cursor: 'é'.repeat(257),
    targets: [pageTargets[0]],
  })).toThrow('selection cursor is invalid')

  const selection = createOperatorSelection('rules')
  selection.applySnapshot(parseOperatorSelectionSnapshot({
    domain: 'rules',
    kind: 'frozen_filter',
    selection_token: '4e0add48-42c9-4f4f-b5c7-f89ebf2a6ee6',
    filter_fingerprint: digest('f'),
    target_count: 0,
    excluded_ids: ['rule-1'],
    expires_at: '2026-09-10T12:00:00.000Z',
    reconfirmation_required: false,
  }))
  expect(selection.selectedCount.value).toBe(0)
})
