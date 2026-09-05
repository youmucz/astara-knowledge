/**
 * Embedded surface inventory tests (task 2.4).
 *
 * Prove that the embedded build graph cannot navigate to — and never even
 * references — disabled WeKnora surfaces: login/registration, tenant
 * creation/switching, global user controls, platform infrastructure
 * settings, chat, agents, organizations, and the embed widget entry.
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { createEmbeddedRouter, embeddedRouteGuard } from './router'
import { isEmbeddedRouteAdmitted, EMBEDDED_DEFAULT_ROUTE } from './contract'

const here = dirname(fileURLToPath(import.meta.url))

/** Disabled view modules that must never appear in the embedded graph. */
const FORBIDDEN_VIEW_REFERENCES = [
  'views/auth/',
  'views/settings/Settings',
  'views/agent/',
  'views/organization/',
  'views/chat/',
  'views/creatChat/',
  'views/platform/',
  'views/embed/',
  'components/menu.vue',
  'components/UserMenu',
  'GlobalInvitationBell',
  'NewUserGuide',
]

/** Embedded-surface sources whose import graph is inventoried. */
const EMBEDDED_SURFACE_SOURCES = [
  resolve(here, 'embedded-main.ts'),
  resolve(here, 'EmbeddedApp.vue'),
  resolve(here, 'router.ts'),
]

test('embedded route table admits only the knowledge surface', () => {
  const router = createEmbeddedRouter()
  const records = router.getRoutes()
  assert.ok(records.length > 0)
  // Component records (no redirect) must all stay inside the admitted
  // knowledge prefixes; the two redirect records ('/' and the catch-all)
  // are themselves default-deny sinks.
  const componentRecords = records.filter((record) => record.redirect === undefined)
  assert.ok(componentRecords.length >= 2)
  for (const record of componentRecords) {
    assert.equal(
      isEmbeddedRouteAdmitted(record.path),
      true,
      `non-admitted route registered: ${record.path}`,
    )
  }
  const names = records.map((record) => record.name?.toString() ?? '')
  assert.ok(names.includes('knowledgeBaseList'))
  assert.ok(names.includes('knowledgeBaseDetail'))
  // Disabled standalone surfaces are not even registered.
  for (const forbidden of ['login', 'registerByInvite', 'settings', 'agentList', 'organizationList', 'chat', 'globalCreatChat']) {
    assert.equal(names.includes(forbidden), false, `disabled route registered: ${forbidden}`)
  }
})

test('embedded router never resolves a disabled deep link', () => {
  // The guard itself is default-deny: every disabled surface is redirected
  // to the knowledge list before any component loads.
  for (const denied of ['/login', '/platform/agents', '/platform/settings', '/platform/organizations', '/platform/chat/x', '/platform/creatChat', '/onboarding/workspace', '/register']) {
    assert.equal(embeddedRouteGuard({ path: denied }), EMBEDDED_DEFAULT_ROUTE, `deep link not denied: ${denied}`)
  }
  assert.equal(embeddedRouteGuard({ path: '/platform/knowledge-bases' }), true)
  assert.equal(embeddedRouteGuard({ path: '/platform/knowledge-bases/42' }), true)
})

test('embedded surface sources contain no reference to disabled views', () => {
  for (const sourcePath of EMBEDDED_SURFACE_SOURCES) {
    const source = readFileSync(sourcePath, 'utf8')
    for (const forbidden of FORBIDDEN_VIEW_REFERENCES) {
      assert.equal(
        source.includes(forbidden),
        false,
        `embedded surface references disabled module "${forbidden}" in ${sourcePath}`,
      )
    }
  }
})

test('embedded entry mounts exactly the knowledge views', () => {
  const routerSource = readFileSync(resolve(here, 'router.ts'), 'utf8')
  // The only lazy-loaded views are the knowledge list and detail screens.
  const lazyImports = [...routerSource.matchAll(/import\('([^']+)'\)/g)].map((m) => m[1])
  assert.deepEqual(lazyImports, ['../views/knowledge/KnowledgeBaseList.vue', '../views/knowledge/KnowledgeBase.vue'])
})
