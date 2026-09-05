/**
 * Runtime context for the embedded (Plane-hosted) surface.
 *
 * `useEmbeddedSurface()` is safe to call from any component: outside
 * Embedded Mode it returns the `standalone` projection so shared views keep
 * their full behaviour; inside Embedded Mode every capability check is
 * default-deny against the host-admitted list.
 */

import { inject, computed } from 'vue'
import {
  EMBEDDED_CAPABILITY_KEYS,
  isEmbeddedCapabilityAdmitted,
  type EmbeddedCapabilityKey,
} from './contract'
import { isEmbeddedMode } from './mode'

export interface EmbeddedSurfaceContext {
  capabilities: () => readonly string[]
}

export const EMBEDDED_SURFACE_KEY = Symbol('weknora-embedded-surface')

export interface EmbeddedSurfaceProjection {
  mode: 'embedded' | 'standalone'
  isCapabilityAdmitted: (key: EmbeddedCapabilityKey) => boolean
}

export function useEmbeddedSurface(): EmbeddedSurfaceProjection {
  const injected = inject<EmbeddedSurfaceContext | null>(EMBEDDED_SURFACE_KEY, null)
  const embedded = isEmbeddedMode()
  const capabilities = computed(() => injected?.capabilities() ?? [])
  const projection: EmbeddedSurfaceProjection = {
    mode: embedded ? 'embedded' : 'standalone',
    isCapabilityAdmitted: (key) => {
      if (!embedded) return true
      return isEmbeddedCapabilityAdmitted(capabilities.value, key)
    },
  }
  return projection
}

export const ALL_EMBEDDED_CAPABILITY_KEYS: readonly EmbeddedCapabilityKey[] = EMBEDDED_CAPABILITY_KEYS
