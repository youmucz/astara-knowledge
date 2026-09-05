/**
 * Embedded request cancellation.
 *
 * In Embedded Mode every in-flight request is bound to a single shared
 * AbortController. `resetEmbeddedRequestScope()` arms a fresh controller at
 * mount; `abortEmbeddedRequests()` cancels everything still pending at
 * unmount so a detached remote can never resolve work into a dead container.
 */

let embeddedAbortController: AbortController | null = null;

export function resetEmbeddedRequestScope(): void {
  embeddedAbortController = new AbortController()
}

export function abortEmbeddedRequests(): void {
  embeddedAbortController?.abort()
  embeddedAbortController = null
}

export function currentEmbeddedAbortSignal(): AbortSignal | null {
  return embeddedAbortController?.signal ?? null
}
