/**
 * Embedded Mode flag + session-expiry notification tests.
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  activateEmbeddedMode,
  isEmbeddedMode,
  setEmbeddedSessionExpiredHandler,
  notifyEmbeddedSessionExpired,
  EMBEDDED_SESSION_SENTINEL,
} from './mode'

// Module state is process-global: activate once and keep it on — the
// embedded artifact runs with the flag latched for its whole lifetime.
test('embedded mode flag latches on', () => {
  if (!isEmbeddedMode()) {
    activateEmbeddedMode()
  }
  assert.equal(isEmbeddedMode(), true)
})

test('session expired notification reaches the registered handler exactly once', () => {
  let calls = 0
  setEmbeddedSessionExpiredHandler(() => {
    calls += 1
  })
  notifyEmbeddedSessionExpired()
  assert.equal(calls, 1)
})

test('a throwing handler never breaks the notification path', () => {
  setEmbeddedSessionExpiredHandler(() => {
    throw new Error('host callback exploded')
  })
  assert.doesNotThrow(() => notifyEmbeddedSessionExpired())
})

test('clearing the handler silences notifications', () => {
  let calls = 0
  setEmbeddedSessionExpiredHandler(() => {
    calls += 1
  })
  setEmbeddedSessionExpiredHandler(null)
  notifyEmbeddedSessionExpired()
  assert.equal(calls, 0)
})

test('the session sentinel is a non-credential marker', () => {
  assert.equal(typeof EMBEDDED_SESSION_SENTINEL, 'string')
  assert.equal(EMBEDDED_SESSION_SENTINEL.includes('Bearer'), false)
})
