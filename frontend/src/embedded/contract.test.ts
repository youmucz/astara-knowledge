/**
 * Contract tests for the versioned embedded UI bootstrap (task 1.1).
 *
 * These tests are the executable form of UI contract version 1: validation
 * is fail-closed and route/capability evaluation is default-deny.
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  KNOWLEDGE_UI_CONTRACT_VERSION,
  EMBEDDED_DEFAULT_ROUTE,
  EMBEDDED_CAPABILITY_KEYS,
  validateEmbeddedProps,
  normalizeEmbeddedRoute,
  hostToRemoteRoute,
  remoteToHostRoute,
  isEmbeddedRouteAdmitted,
  isEmbeddedCapabilityAdmitted,
  normalizeEmbeddedTheme,
  type KnowledgeEmbeddedProps,
} from './contract'

function validProps(overrides: Partial<Record<string, unknown>> = {}): Record<string, unknown> {
  return {
    contractVersion: KNOWLEDGE_UI_CONTRACT_VERSION,
    workspace: { id: 'ws-1', slug: 'acme', name: 'Acme' },
    route: '/platform/knowledge-bases',
    theme: 'light',
    locale: 'en-US',
    capabilities: ['knowledge-bases', 'search'],
    exchange: { assertion: 'one-time-assertion' },
    callbacks: {
      onNavigate: () => {},
      onOpenSource: () => {},
      onError: () => {},
      onSessionExpired: () => {},
    },
    ...overrides,
  } as Record<string, unknown>
}

test('valid bootstrap props pass validation', () => {
  assert.equal(validateEmbeddedProps(validProps()).valid, true)
})

test('wrong contract version fails closed', () => {
  const result = validateEmbeddedProps(validProps({ contractVersion: KNOWLEDGE_UI_CONTRACT_VERSION + 1 }))
  assert.equal(result.valid, false)
  assert.equal(result.code, 'contract_version_mismatch')
})

test('missing workspace fields fail closed', () => {
  assert.equal(validateEmbeddedProps(validProps({ workspace: {} })).code, 'workspace_id_invalid')
  assert.equal(validateEmbeddedProps(validProps({ workspace: { id: '', slug: 'a', name: 'b' } })).code, 'workspace_id_invalid')
})

test('invalid theme and locale fail closed', () => {
  assert.equal(validateEmbeddedProps(validProps({ theme: 'sepia' })).code, 'theme_invalid')
  assert.equal(validateEmbeddedProps(validProps({ locale: 'not a locale' })).code, 'locale_invalid')
})

test('unknown capability keys fail closed', () => {
  const result = validateEmbeddedProps(validProps({ capabilities: ['knowledge-bases', 'agents'] }))
  assert.equal(result.valid, false)
  assert.equal(result.code, 'capability_unknown')
})

test('missing exchange assertion fails closed', () => {
  assert.equal(validateEmbeddedProps(validProps({ exchange: {} })).code, 'exchange_missing')
})

test('missing callbacks fail closed', () => {
  const props = validProps() as { callbacks: Record<string, unknown> }
  delete props.callbacks.onSessionExpired
  assert.equal(validateEmbeddedProps(props).code, 'callback_onSessionExpired_missing')
})

test('route normalization is default-deny for disabled surfaces', () => {
  // Only the knowledge surface is admitted; login/settings/agents/organizations/
  // chat/system routes all fall back to the knowledge list.
  for (const denied of [
    '/login',
    '/register',
    '/platform/settings',
    '/platform/agents',
    '/platform/organizations',
    '/platform/chat/abc',
    '/platform/creatChat',
    '/platform/system/settings',
    '/knowledgeBase',
    'https://evil.example.com/platform/knowledge-bases',
  ]) {
    assert.equal(normalizeEmbeddedRoute(denied), EMBEDDED_DEFAULT_ROUTE, `route not denied: ${denied}`)
  }
  assert.equal(normalizeEmbeddedRoute('/platform/knowledge-bases/42'), '/platform/knowledge-bases/42')
  assert.equal(normalizeEmbeddedRoute('/platform/knowledge-bases/42?tab=wiki'), '/platform/knowledge-bases/42')
  assert.equal(normalizeEmbeddedRoute(''), EMBEDDED_DEFAULT_ROUTE)
  assert.equal(normalizeEmbeddedRoute(undefined), EMBEDDED_DEFAULT_ROUTE)
})

test('host and remote route mappings round-trip', () => {
  assert.equal(hostToRemoteRoute('/knowledge-bases/42'), '/platform/knowledge-bases/42')
  assert.equal(hostToRemoteRoute('/platform/knowledge-bases/42'), '/platform/knowledge-bases/42')
  assert.equal(hostToRemoteRoute('/platform/agents'), EMBEDDED_DEFAULT_ROUTE)
  assert.equal(remoteToHostRoute('/platform/knowledge-bases/42'), '/knowledge-bases/42')
  assert.equal(remoteToHostRoute('/platform/agents'), null)
})

test('capability evaluation is default-deny', () => {
  assert.equal(isEmbeddedCapabilityAdmitted(['knowledge-bases'], 'knowledge-bases'), true)
  assert.equal(isEmbeddedCapabilityAdmitted(['knowledge-bases'], 'wiki'), false)
  assert.equal(isEmbeddedCapabilityAdmitted(undefined, 'knowledge-bases'), false)
  assert.equal(isEmbeddedCapabilityAdmitted([], 'search'), false)
})

test('every declared capability key is admitted by its own name', () => {
  for (const key of EMBEDDED_CAPABILITY_KEYS) {
    assert.equal(isEmbeddedCapabilityAdmitted([key], key), true)
  }
})

test('theme normalization only admits light and dark', () => {
  assert.equal(normalizeEmbeddedTheme('dark'), 'dark')
  assert.equal(normalizeEmbeddedTheme('light'), 'light')
  assert.equal(normalizeEmbeddedTheme('system'), 'light')
  assert.equal(normalizeEmbeddedTheme(undefined), 'light')
})

test('admission helper matches only admitted prefixes', () => {
  assert.equal(isEmbeddedRouteAdmitted('/platform/knowledge-bases'), true)
  assert.equal(isEmbeddedRouteAdmitted('/platform/knowledge-bases/x/y'), true)
  assert.equal(isEmbeddedRouteAdmitted('/platform/knowledge-bases-other'), false)
  assert.equal(isEmbeddedRouteAdmitted('/platform'), false)
})
