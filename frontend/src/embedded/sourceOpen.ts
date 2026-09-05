/**
 * Typed source-opening bridge between the embedded Knowledge views and the
 * Plane host.
 *
 * Mirrors the session-expiry handler pattern in mode.ts: the embedded
 * entrypoint registers the host-provided `onOpenSource` callback at mount
 * time (and refreshes it on update), and any view can request that a native
 * Plane source be opened — in Embedded Mode the host navigates to the
 * canonical Plane Page editor; in standalone mode the request is a no-op so
 * shared views keep their full behaviour.
 *
 * Only Plane-owned source kinds exist (see KnowledgeEmbeddedSourceRef in
 * contract.ts); unknown sources are ignored rather than guessed.
 */

import { isEmbeddedMode } from './mode'
import type { KnowledgeEmbeddedSourceRef } from './contract'

let openSourceHandler: ((source: KnowledgeEmbeddedSourceRef) => void) | null = null

export function setEmbeddedSourceOpenHandler(
  handler: ((source: KnowledgeEmbeddedSourceRef) => void) | null,
): void {
  openSourceHandler = handler
}

/**
 * Ask the Plane host to open a native source. Returns true when the request
 * was handed to the host (Embedded Mode with a live handler); false when the
 * caller should fall back to in-app behaviour.
 */
export function requestEmbeddedSourceOpen(source: KnowledgeEmbeddedSourceRef): boolean {
  if (!isEmbeddedMode() || openSourceHandler === null) {
    return false
  }
  if (source?.type !== 'plane_page' || !source.pageId) {
    return false
  }
  try {
    openSourceHandler(source)
    return true
  } catch {
    /* never let a host callback throw into the view layer */
    return false
  }
}
