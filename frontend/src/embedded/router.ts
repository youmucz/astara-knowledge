/**
 * Knowledge-only route table for Embedded Mode.
 *
 * The paths intentionally match the standalone app's knowledge routes
 * (`/platform/knowledge-bases/...`) so in-view `router.push(...)` calls keep
 * working. Everything outside the admitted prefixes is redirected
 * (default-deny) — login/register/settings/agents/organizations routes are
 * never even registered, so their chunks are never imported.
 */

import { createRouter, createMemoryHistory, type Router } from 'vue-router'
import { EMBEDDED_DEFAULT_ROUTE, isEmbeddedRouteAdmitted } from './contract'

/**
 * Default-deny guard: any target outside the admitted knowledge prefixes
 * (disabled deep links, tampered memory state) is redirected to the
 * knowledge list. Exported for direct unit testing.
 */
export function embeddedRouteGuard(to: { path: string }): string | boolean {
  return isEmbeddedRouteAdmitted(to.path) ? true : EMBEDDED_DEFAULT_ROUTE
}

/**
 * Build the embedded router. The caller seeds the initial route (the host
 * URL) with an explicit `router.push(...)` so constructing the route table
 * never triggers component loading.
 */
export function createEmbeddedRouter(): Router {
  const router = createRouter({
    // The remote never owns top-level browser history: Plane owns the URL
    // and feeds route changes back through update({ route }).
    history: createMemoryHistory(),
    routes: [
      { path: '/', redirect: EMBEDDED_DEFAULT_ROUTE },
      {
        path: '/platform/knowledge-bases',
        name: 'knowledgeBaseList',
        component: () => import('../views/knowledge/KnowledgeBaseList.vue'),
        meta: { embedded: true },
      },
      {
        path: '/platform/knowledge-bases/:kbId',
        name: 'knowledgeBaseDetail',
        component: () => import('../views/knowledge/KnowledgeBase.vue'),
        meta: { embedded: true },
      },
      // Default-deny catch-all: unknown/deep-linked disabled routes land on
      // the admitted knowledge list instead of a dead screen.
      { path: '/:pathMatch(.*)*', redirect: () => EMBEDDED_DEFAULT_ROUTE },
    ],
  })

  router.beforeEach(embeddedRouteGuard)

  return router
}
