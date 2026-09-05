/**
 * Tests for the source-opening bridge (embedded/sourceOpen.ts).
 *
 * Verifies the task-4.2 semantics: a plane_page source is handed to the host
 * callback in Embedded Mode; standalone mode and malformed sources fall back
 * to in-app behaviour; a throwing host callback never escapes into the view.
 */

import assert from 'node:assert/strict'
import { test, beforeEach } from 'node:test'

import {
  requestEmbeddedSourceOpen,
  setEmbeddedSourceOpenHandler,
} from './sourceOpen'
import { activateEmbeddedMode } from './mode'

beforeEach(() => {
  setEmbeddedSourceOpenHandler(null)
})

test('standalone mode never hands sources to a host', () => {
  const seen: Array<Record<string, unknown>> = []
  setEmbeddedSourceOpenHandler((source) => seen.push({ ...source }))
  // Standalone: isEmbeddedMode() is false in the test process unless activated.
  const handled = requestEmbeddedSourceOpen({ type: 'plane_page', pageId: 'page-1' })
  assert.equal(handled, false)
  assert.equal(seen.length, 0)
})

test('embedded mode hands plane_page sources to the host with project metadata', () => {
  activateEmbeddedMode()
  const seen: Array<Record<string, unknown>> = []
  setEmbeddedSourceOpenHandler((source) => seen.push({ ...source }))
  const handled = requestEmbeddedSourceOpen({
    type: 'plane_page',
    pageId: 'page-1',
    projectId: 'proj-9',
  })
  assert.equal(handled, true)
  assert.equal(seen.length, 1)
  assert.equal(seen[0].type, 'plane_page')
  assert.equal(seen[0].pageId, 'page-1')
  assert.equal(seen[0].projectId, 'proj-9')
})

test('embedded mode without a handler falls back', () => {
  activateEmbeddedMode()
  setEmbeddedSourceOpenHandler(null)
  const handled = requestEmbeddedSourceOpen({ type: 'plane_page', pageId: 'page-1' })
  assert.equal(handled, false)
})

test('unknown or malformed sources are ignored', () => {
  activateEmbeddedMode()
  const seen: unknown[] = []
  setEmbeddedSourceOpenHandler((source) => seen.push(source))
  assert.equal(requestEmbeddedSourceOpen({ type: 'plane_page', pageId: '' }), false)
  assert.equal(seen.length, 0)
})

test('a throwing host callback never escapes into the view layer', () => {
  activateEmbeddedMode()
  setEmbeddedSourceOpenHandler(() => {
    throw new Error('host navigation exploded')
  })
  const handled = requestEmbeddedSourceOpen({ type: 'plane_page', pageId: 'page-2' })
  assert.equal(handled, false)
})

test('handler replacement takes effect without remount', () => {
  activateEmbeddedMode()
  const first: string[] = []
  const second: string[] = []
  setEmbeddedSourceOpenHandler((source) => first.push(source.pageId))
  assert.equal(requestEmbeddedSourceOpen({ type: 'plane_page', pageId: 'a' }), true)
  setEmbeddedSourceOpenHandler((source) => second.push(source.pageId))
  assert.equal(requestEmbeddedSourceOpen({ type: 'plane_page', pageId: 'b' }), true)
  assert.deepEqual(first, ['a'])
  assert.deepEqual(second, ['b'])
})
