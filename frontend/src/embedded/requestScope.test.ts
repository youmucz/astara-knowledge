/**
 * Embedded request-scope tests: unmount aborts every in-flight request of
 * the detached remote (task 1.3/1.4 — request cancellation scoped to the
 * mounted root).
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  resetEmbeddedRequestScope,
  abortEmbeddedRequests,
  currentEmbeddedAbortSignal,
} from './requestScope'

test('a fresh scope arms a live signal', () => {
  resetEmbeddedRequestScope()
  const signal = currentEmbeddedAbortSignal()
  assert.ok(signal)
  assert.equal(signal!.aborted, false)
})

test('abort cancels the current scope and clears the signal', () => {
  resetEmbeddedRequestScope()
  const signal = currentEmbeddedAbortSignal()
  abortEmbeddedRequests()
  assert.ok(signal)
  assert.equal(signal!.aborted, true)
  assert.equal(currentEmbeddedAbortSignal(), null)
})

test('remounting after abort arms a fresh, unaborted signal', () => {
  resetEmbeddedRequestScope()
  const oldSignal = currentEmbeddedAbortSignal()
  abortEmbeddedRequests()
  resetEmbeddedRequestScope()
  const newSignal = currentEmbeddedAbortSignal()
  assert.ok(newSignal)
  assert.notEqual(newSignal, oldSignal)
  assert.equal(newSignal!.aborted, false)
  assert.equal(oldSignal!.aborted, true)
})

test('aborting without a scope is a safe no-op', () => {
  abortEmbeddedRequests()
  assert.equal(currentEmbeddedAbortSignal(), null)
})
