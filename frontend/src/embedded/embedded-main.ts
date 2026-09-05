/**
 * Embedded Mode entrypoint — the Plane-hosted Knowledge microfrontend.
 *
 * Exports the versioned mount/update/unmount contract (see ./contract.ts).
 * This module is built as a self-contained ESM artifact by
 * `vite.config.embedded.ts`; Plane loads it from the reserved same-origin
 * `/knowledge-assets/` path and never executes it before the reported
 * contract version matches runtime readiness.
 */

import { createApp, type App as VueApp, type Component } from 'vue'
import { createPinia, type Pinia } from 'pinia'
import TDesign, { MessagePlugin } from 'tdesign-vue-next'
import 'tdesign-vue-next/es/style/index.css'
import '@/assets/fonts.css'
import '@/assets/theme/theme.css'
import 'vue-virtual-scroller/dist/vue-virtual-scroller.css'
import { installTDesignIconOfflineGuard } from '@/utils/tdesign-icon-offline'
import i18n from '@/i18n'

import EmbeddedApp from './EmbeddedApp.vue'
import {
  EMBEDDED_API_BASE,
  EMBEDDED_PORTAL_ID,
  EMBEDDED_PORTAL_SELECTOR,
  EMBEDDED_ROOT_ATTRIBUTE,
  EMBEDDED_ROOT_VALUE,
  KNOWLEDGE_UI_CONTRACT_VERSION,
  hostToRemoteRoute,
  normalizeEmbeddedTheme,
  remoteToHostRoute,
  validateEmbeddedProps,
  type KnowledgeEmbeddedCallbacks,
  type KnowledgeEmbeddedHandle,
  type KnowledgeEmbeddedProps,
} from './contract'
import { activateEmbeddedMode, setEmbeddedSessionExpiredHandler } from './mode'
import { setApiBaseOverride } from '@/utils/api-base'
import { bootstrapEmbeddedSession } from './session'
import { createEmbeddedRouter } from './router'
import { abortEmbeddedRequests, resetEmbeddedRequestScope } from './requestScope'

export { KNOWLEDGE_UI_CONTRACT_VERSION } from './contract'
export type {
  KnowledgeEmbeddedProps,
  KnowledgeEmbeddedHandle,
  KnowledgeEmbeddedCallbacks,
} from './contract'

installTDesignIconOfflineGuard()

export class EmbeddedBootstrapError extends Error {
  constructor(readonly code: string) {
    super(code)
    this.name = 'EmbeddedBootstrapError'
  }
}

/* ------------------------------------------------------------------ */
/* MessagePlugin portal attachment                                     */
/* ------------------------------------------------------------------ */

type MessagePatchMethod = 'info' | 'success' | 'warning' | 'error' | 'question' | 'loading'

const messagePatchApplied = new WeakSet<object>()

/**
 * MessagePlugin toasts default to attaching on `document.body`, which would
 * escape the scoped embedded subtree. Patch each variant once so every toast
 * renders inside the portal root. Idempotent per MessagePlugin instance.
 */
function patchMessageAttach(): void {
  const holder = MessagePlugin as unknown as Record<string, unknown>
  if (!holder || typeof holder !== 'object' || messagePatchApplied.has(holder)) return
  for (const method of ['info', 'success', 'warning', 'error', 'question', 'loading'] as MessagePatchMethod[]) {
    const original = holder[method]
    if (typeof original !== 'function') continue
    holder[method] = function patched(this: unknown, ...args: unknown[]) {
      const first = args[0]
      const options =
        typeof first === 'string' || typeof first === 'number'
          ? { content: first, attach: EMBEDDED_PORTAL_SELECTOR }
          : { attach: EMBEDDED_PORTAL_SELECTOR, ...(first as Record<string, unknown> | undefined) }
      return (original as (...a: unknown[]) => unknown).apply(this, [options, ...args.slice(1)])
    }
  }
  messagePatchApplied.add(holder)
}

/* ------------------------------------------------------------------ */
/* Portal element                                                      */
/* ------------------------------------------------------------------ */

function ensurePortal(): HTMLElement {
  let portal = document.getElementById(EMBEDDED_PORTAL_ID)
  if (!portal) {
    portal = document.createElement('div')
    portal.id = EMBEDDED_PORTAL_ID
    portal.setAttribute(EMBEDDED_ROOT_ATTRIBUTE, EMBEDDED_ROOT_VALUE)
    document.body.appendChild(portal)
  }
  return portal
}

/* ------------------------------------------------------------------ */
/* mount / update / unmount                                            */
/* ------------------------------------------------------------------ */

interface EmbeddedMountInternals {
  app: VueApp<Element>
  pinia: Pinia
  rootProps: { theme: string; capabilities: readonly string[] }
  workspaceId: string
  removeAfterEach: () => void
  portal: HTMLElement
  unmounted: boolean
}

export async function mount(
  container: HTMLElement,
  props: KnowledgeEmbeddedProps,
): Promise<KnowledgeEmbeddedHandle> {
  activateEmbeddedMode()
  setApiBaseOverride(EMBEDDED_API_BASE)
  patchMessageAttach()

  const validation = validateEmbeddedProps(props)
  if (!validation.valid) {
    throw new EmbeddedBootstrapError(validation.code ?? 'props_invalid')
  }

  resetEmbeddedRequestScope()
  const portal = ensurePortal()
  const theme = normalizeEmbeddedTheme(props.theme)
  portal.setAttribute('theme-mode', theme)

  // 1. Exchange the one-time assertion for the HttpOnly session cookie and
  //    hydrate the in-memory auth store before any view renders.
  const exchange = await bootstrapEmbeddedSession(props.exchange.assertion)
  if (!exchange.ok) {
    setEmbeddedSessionExpiredHandler(null)
    throw new EmbeddedBootstrapError(exchange.code ?? 'exchange_failed')
  }

  // 2. Build the app: knowledge-only routes under a memory history that the
  //    host drives; theme/capabilities flow through reactive root props.
  const router = createEmbeddedRouter()
  const pinia = createPinia()
  const rootProps = { theme, capabilities: props.capabilities }
  const app = createApp(EmbeddedApp as unknown as Component, rootProps)
  app.use(TDesign)
  app.use(pinia)
  app.use(router)
  app.use(i18n)
  i18n.global.locale.value = props.locale as typeof i18n.global.locale.value

  let currentRemoteRoute = hostToRemoteRoute(props.route)
  // Navigation reporting starts after the initial route is seeded: the host
  // already knows its own URL, so only user-driven navigation is reported.
  let reporting = false
  const reportNavigation = (path: string) => {
    if (!reporting) return
    const hostRoute = remoteToHostRoute(path)
    if (hostRoute !== null) {
      currentRemoteRoute = path
      callbacks.onNavigate(hostRoute)
    }
  }
  const removeAfterEach = router.afterEach((to) => reportNavigation(to.path))

  let callbacks: KnowledgeEmbeddedCallbacks = props.callbacks
  setEmbeddedSessionExpiredHandler(() => callbacks.onSessionExpired())

  // Seed the memory history with the host URL, then wait for the first
  // navigation (component load) to settle before mounting.
  await router.push(currentRemoteRoute)
  await router.isReady()
  app.mount(container)
  reporting = true
  callbacks.onReady?.()

  const internals: EmbeddedMountInternals = {
    app,
    pinia,
    rootProps,
    workspaceId: props.workspace.id,
    removeAfterEach,
    portal,
    unmounted: false,
  }

  return {
    update(patch) {
      if (internals.unmounted) {
        throw new EmbeddedBootstrapError('update_after_unmount')
      }
      if (patch.callbacks) {
        callbacks = patch.callbacks
        setEmbeddedSessionExpiredHandler(() => callbacks.onSessionExpired())
      }
      if (patch.workspace && patch.workspace.id !== internals.workspaceId) {
        // Workspace switches MUST go through unmount + mount so no tenant
        // state can leak between workspaces (spec: deterministic cleanup).
        callbacks.onError({ code: 'workspace_change_requires_remount' })
        return
      }
      if (patch.theme !== undefined) {
        const nextTheme = normalizeEmbeddedTheme(patch.theme)
        internals.rootProps.theme = nextTheme
        internals.portal.setAttribute('theme-mode', nextTheme)
      }
      if (patch.locale !== undefined) {
        i18n.global.locale.value = patch.locale as typeof i18n.global.locale.value
      }
      if (patch.capabilities !== undefined) {
        internals.rootProps.capabilities = patch.capabilities
      }
      if (patch.exchange?.assertion) {
        // Host re-bootstrapped after session expiry: exchange the fresh
        // assertion in place (no remount needed).
        void bootstrapEmbeddedSession(patch.exchange.assertion).then((result) => {
          if (!result.ok) {
            callbacks.onSessionExpired()
          }
        })
      }
      if (patch.route !== undefined) {
        const nextRoute = hostToRemoteRoute(patch.route)
        if (nextRoute !== currentRemoteRoute) {
          void router.replace(nextRoute).catch(() => {
            /* navigation guards may redirect; afterEach still reports */
          })
        }
      }
    },

    async unmount() {
      if (internals.unmounted) return
      internals.unmounted = true
      // Cancel every in-flight request of the detached remote before any
      // teardown so no callback can resolve into a dead container.
      abortEmbeddedRequests()
      setEmbeddedSessionExpiredHandler(null)
      internals.removeAfterEach()
      try {
        internals.app.unmount()
      } finally {
        internals.portal.remove()
      }
    },
  }
}
