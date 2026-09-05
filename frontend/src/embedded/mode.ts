/**
 * Process-wide Embedded Mode flag.
 *
 * Activated exactly once by the embedded entrypoint before any Pinia store,
 * router guard or axios interceptor runs. Downstream modules consult
 * `isEmbeddedMode()` to:
 *
 *  - keep every WeKnora credential out of localStorage/sessionStorage;
 *  - stop attaching `Authorization`/`X-Tenant-ID` headers (the embedded
 *    session is a short-lived HttpOnly cookie pinned to one tenant);
 *  - stop redirecting to WeKnora's own /login page on auth failure.
 */

let embeddedActive = false;

export function activateEmbeddedMode(): void {
  embeddedActive = true;
}

export function isEmbeddedMode(): boolean {
  return embeddedActive;
}

/**
 * Sentinel stored in the (in-memory) auth store so `isLoggedIn` holds while
 * the real credential lives in an HttpOnly cookie. It is never persisted and
 * — because the axios layer checks `isEmbeddedMode()` first — never sent on
 * the wire.
 */
export const EMBEDDED_SESSION_SENTINEL = 'embedded-session';

/**
 * Session-expiry notification: the axios layer rejects embedded 401s with
 * code SESSION_EXPIRED and pings this handler so the host can re-bootstrap
 * without any view-level handling.
 */
let sessionExpiredHandler: (() => void) | null = null;

export function setEmbeddedSessionExpiredHandler(handler: (() => void) | null): void {
  sessionExpiredHandler = handler;
}

export function notifyEmbeddedSessionExpired(): void {
  try {
    sessionExpiredHandler?.();
  } catch {
    /* never let a host callback throw into the request layer */
  }
}
