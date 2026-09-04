export const DEPLOYMENT_CAPABILITY_KEYS = [
  'organizations',
  'agents',
  'integrations.im',
  'integrations.embed',
  'integrations.api',
  'settings.mcp',
  'settings.websearch',
  'settings.vectorstore',
  'settings.storage',
  'settings.sandbox',
  'settings.sandbox.docker',
] as const

export type DeploymentCapabilityKey = typeof DEPLOYMENT_CAPABILITY_KEYS[number]

export interface DeploymentCapability {
  supported: boolean
  reason?: string
}

export type DeploymentCapabilityMap = Partial<Record<DeploymentCapabilityKey, DeploymentCapability>>

/**
 * Capability discovery is fail closed. Missing/unknown keys never expose a
 * route that the effective backend route table did not advertise.
 */
export function isDeploymentCapabilitySupported(
  capabilities: DeploymentCapabilityMap,
  key?: DeploymentCapabilityKey,
  options?: { liteMode?: boolean; edition?: string },
): boolean {
  if (!key) return true
  if (key === 'organizations') {
    const isLite =
      options?.liteMode === true ||
      options?.edition?.trim().toLowerCase() === 'lite'
    if (isLite) return false
  }
  // Docker talks to a local Engine API (often docker.sock = host root), so
  // missing or failed capability probes must not leave the picker visible.
  if (key === 'settings.sandbox.docker') {
    return capabilities[key]?.supported === true
  }
  return capabilities[key]?.supported === true
}

export const SETTINGS_SECTION_CAPABILITY: Partial<Record<string, DeploymentCapabilityKey>> = {
  websearch: 'settings.websearch',
  vectorstore: 'settings.vectorstore',
  storage: 'settings.storage',
  sandbox: 'settings.sandbox',
  // Skills are baked into a sandbox image. Hide the catalog when the
  // deployment has no sandbox support, same as personal skill credentials.
  skills: 'settings.sandbox',
  // Skill credentials exist only because sandboxes do: the values are injected
  // into a skill script's process. A deployment without sandbox support has
  // nowhere to inject them, so the page would only ever show its empty state.
  envvars: 'settings.sandbox',
  mcp: 'settings.mcp',
}
