/**
 * Embedded session bootstrap.
 *
 * The Plane host hands the remote a one-time signed assertion. The remote
 * exchanges it — same-origin, through the /api/knowledge proxy prefix — for
 * a short-lived HttpOnly Knowledge session cookie, then hydrates the in-memory
 * auth store from /auth/me. No WeKnora credential ever enters JS-accessible
 * storage.
 */

import { post } from '@/utils/request'
import { getCurrentUser } from '@/api/auth'
import { useAuthStore } from '@/stores/auth'
import type { UserInfo, TenantInfo } from '@/api/auth'

export const EMBEDDED_EXCHANGE_ENDPOINT = '/api/v1/astara/identity/exchange'

export interface EmbeddedExchangeResult {
  ok: boolean
  expiresAt?: string
  code?: string
}

/** Exchange a one-time Plane assertion for the HttpOnly session cookie. */
export async function exchangeEmbeddedSession(assertion: string): Promise<EmbeddedExchangeResult> {
  try {
    const response = (await post(EMBEDDED_EXCHANGE_ENDPOINT, { assertion })) as unknown as {
      success?: boolean
      data?: { expires_at?: string }
    }
    if (response && (response.success === undefined || response.success === true)) {
      return { ok: true, expiresAt: response.data?.expires_at }
    }
    return { ok: false, code: 'exchange_rejected' }
  } catch (error: unknown) {
    const code =
      typeof error === 'object' && error !== null && 'code' in error
        ? String((error as { code?: unknown }).code)
        : 'exchange_failed'
    return { ok: false, code }
  }
}

/**
 * Hydrate the embedded auth store from /auth/me. Returns false when the
 * session is missing/expired so the host can re-bootstrap.
 */
export async function hydrateEmbeddedSession(): Promise<boolean> {
  const authStore = useAuthStore()
  const response = await getCurrentUser()
  if (!response.success || !response.data?.user) {
    return false
  }
  const memberships = (response.data.memberships ?? []).map((m) => ({
    tenant_id: m.tenant_id,
    tenant_name: m.tenant_name,
    role: m.role,
  }))
  authStore.setEmbeddedSession({
    user: response.data.user as UserInfo,
    tenant: (response.data.tenant ?? null) as TenantInfo | null,
    memberships,
  })
  return true
}

/** Full bootstrap: exchange the assertion, then hydrate the store. */
export async function bootstrapEmbeddedSession(assertion: string): Promise<EmbeddedExchangeResult> {
  const exchange = await exchangeEmbeddedSession(assertion)
  if (!exchange.ok) {
    return exchange
  }
  const hydrated = await hydrateEmbeddedSession()
  if (!hydrated) {
    return { ok: false, code: 'session_hydration_failed' }
  }
  return exchange
}
