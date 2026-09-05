/**
 * Versioned host/remote contract for the Plane-embedded Knowledge surface.
 *
 * This module is the single source of truth for UI contract version 1. Both
 * sides import it:
 *
 *  - the embedded build (`src/embedded/embedded-main.ts`) exposes
 *    `KNOWLEDGE_UI_CONTRACT_VERSION` and implements `mount`/`update`/`unmount`
 *    against the types below;
 *  - Plane loads the artifact, verifies the reported contract version matches
 *    the version admitted by runtime readiness, and only then mounts.
 *
 * Contract rules (breaking changes require a new version number):
 *  - `mount` is async, resolves once the remote is usable, and rejects on any
 *    contract violation. A container may host at most one mount at a time.
 *  - `update` applies workspace/route/theme/locale/capability changes in
 *    place; it never reloads the artifact or remounts the app.
 *  - `unmount` cancels pending work, removes listeners/timers/portals and
 *    resolves only after the remote root is safe to detach. Unmount is
 *    idempotent.
 *  - The remote never mutates `document.documentElement`, never writes
 *    browser history, and never constructs URLs outside the supplied
 *    workspace scope.
 */

/** Bump on any breaking change to the shapes in this file. */
export const KNOWLEDGE_UI_CONTRACT_VERSION = 1;

/** Attribute on the remote root that scopes every embedded style rule. */
export const EMBEDDED_ROOT_ATTRIBUTE = 'data-weknora-embedded';
/** Value of {@link EMBEDDED_ROOT_ATTRIBUTE}; part of the versioned contract. */
export const EMBEDDED_ROOT_VALUE = 'v1';
/**
 * Id of the body-level portal element the remote creates on mount. Every
 * tdesign popup and teleported modal renders inside it (never directly on
 * `body`) so scoped styles keep applying.
 */
export const EMBEDDED_PORTAL_ID = 'weknora-embedded-portal';
/** Selector form of {@link EMBEDDED_PORTAL_ID} for Teleport/attach targets. */
export const EMBEDDED_PORTAL_SELECTOR = `#${EMBEDDED_PORTAL_ID}`;
/**
 * Same-origin reserved proxy prefix the Plane edge routes to the Knowledge
 * runtime. The embedded build rewrites every API base URL onto it.
 */
export const EMBEDDED_API_BASE = '/api/knowledge';

/** Capability keys the embedded surface understands (default-deny). */
export const EMBEDDED_CAPABILITY_KEYS = [
  'knowledge-bases',
  'documents',
  'folders',
  'uploads',
  'data-sources',
  'sync-status',
  'search',
  'faq',
  'wiki',
] as const;

export type EmbeddedCapabilityKey = (typeof EMBEDDED_CAPABILITY_KEYS)[number];

/** Typed source-opening event. Only Plane-owned source kinds exist. */
export type KnowledgeEmbeddedSourceRef = {
  readonly type: 'plane_page';
  readonly workspaceId?: string;
  readonly workspaceSlug?: string;
  readonly pageId: string;
  readonly projectId?: string;
};

/** Remote path grammar shared with the host (see normalizeEmbeddedRoute). */
export const EMBEDDED_DEFAULT_ROUTE = '/platform/knowledge-bases';

/**
 * The embedded router reuses the standalone app's knowledge route space
 * (`/platform/knowledge-bases/...`) so existing in-view navigation keeps
 * working. Only these prefixes are admitted; everything else is redirected
 * to the default route (default-deny).
 */
const ADMITTED_ROUTE_PREFIXES = ['/platform/knowledge-bases', '/kb'] as const;

export interface KnowledgeEmbeddedCallbacks {
  /** Remote navigated internally; host syncs its own browser URL. */
  onNavigate(path: string): void;
  /** User opened a native Plane source (e.g. a Plane Page). */
  onOpenSource(source: KnowledgeEmbeddedSourceRef): void;
  /** Remote hit a terminal error it cannot recover from. */
  onError(error: { code: string; message?: string }): void;
  /** Embedded Knowledge session expired; host must re-bootstrap. */
  onSessionExpired(): void;
  /** Optional: remote finished initial render. */
  onReady?(): void;
}

export interface KnowledgeEmbeddedExchange {
  /**
   * One-time signed assertion minted by the Plane bootstrap endpoint.
   * The remote exchanges it (same-origin POST) for a short-lived HttpOnly
   * Knowledge session before issuing any data request.
   */
  readonly assertion: string;
}

export interface KnowledgeEmbeddedProps {
  readonly contractVersion: number;
  readonly workspace: {
    readonly id: string;
    readonly slug: string;
    readonly name: string;
  };
  /** Remote route to restore (e.g. "/knowledge-bases" or "/knowledge-bases/42"). */
  readonly route: string;
  readonly theme: 'light' | 'dark';
  readonly locale: string;
  /** Capabilities admitted for this workspace; anything else stays hidden. */
  readonly capabilities: readonly string[];
  readonly exchange: KnowledgeEmbeddedExchange;
  readonly callbacks: KnowledgeEmbeddedCallbacks;
}

export interface KnowledgeEmbeddedHandle {
  /** In-place update; unknown fields are ignored, not merged blindly. */
  update(props: Partial<Omit<KnowledgeEmbeddedProps, 'callbacks'>> & {
    callbacks?: KnowledgeEmbeddedCallbacks;
  }): void;
  /** Deterministic teardown; safe to call more than once. */
  unmount(): Promise<void>;
}

/**
 * Opaque host-provided mount node. The browser build sees an HTMLElement;
 * the contract module itself stays DOM-free so it can be unit-tested and
 * shared with host-side tooling.
 */
export type EmbeddedMountNode = object;

/* ------------------------------------------------------------------ */
/* Validation helpers — shared by the remote entry and the unit tests */
/* ------------------------------------------------------------------ */

const CAPABILITY_SET: ReadonlySet<string> = new Set(EMBEDDED_CAPABILITY_KEYS);
const LOCALE_RE = /^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$/;
const ROUTE_RE = /^\/[A-Za-z0-9\-._~!$&'()*+,;=:@%/]*$/;

export interface EmbeddedPropsValidation {
  valid: boolean;
  code?: string;
}

/** Structural validation of the bootstrap contract (fail closed). */
export function validateEmbeddedProps(input: unknown): EmbeddedPropsValidation {
  if (typeof input !== 'object' || input === null) return { valid: false, code: 'props_not_object' };
  const props = input as Record<string, unknown>;
  if (props.contractVersion !== KNOWLEDGE_UI_CONTRACT_VERSION) {
    return { valid: false, code: 'contract_version_mismatch' };
  }
  const workspace = props.workspace;
  if (typeof workspace !== 'object' || workspace === null) return { valid: false, code: 'workspace_missing' };
  const ws = workspace as Record<string, unknown>;
  for (const key of ['id', 'slug', 'name'] as const) {
    const value = ws[key];
    if (typeof value !== 'string' || value.length === 0 || value.length > 255) {
      return { valid: false, code: `workspace_${key}_invalid` };
    }
  }
  if (typeof props.route !== 'string' || !ROUTE_RE.test(props.route)) {
    return { valid: false, code: 'route_invalid' };
  }
  if (props.theme !== 'light' && props.theme !== 'dark') return { valid: false, code: 'theme_invalid' };
  if (typeof props.locale !== 'string' || !LOCALE_RE.test(props.locale)) {
    return { valid: false, code: 'locale_invalid' };
  }
  if (!Array.isArray(props.capabilities)) return { valid: false, code: 'capabilities_invalid' };
  for (const capability of props.capabilities) {
    if (typeof capability !== 'string' || !CAPABILITY_SET.has(capability)) {
      return { valid: false, code: 'capability_unknown' };
    }
  }
  const exchange = props.exchange;
  if (typeof exchange !== 'object' || exchange === null) {
    return { valid: false, code: 'exchange_missing' };
  }
  const assertion = (exchange as Record<string, unknown>).assertion;
  if (typeof assertion !== 'string' || assertion.length === 0) {
    return { valid: false, code: 'exchange_missing' };
  }
  const callbacks = props.callbacks;
  if (typeof callbacks !== 'object' || callbacks === null) return { valid: false, code: 'callbacks_missing' };
  const cb = callbacks as Record<string, unknown>;
  for (const key of ['onNavigate', 'onOpenSource', 'onError', 'onSessionExpired'] as const) {
    if (typeof cb[key] !== 'function') return { valid: false, code: `callback_${key}_missing` };
  }
  return { valid: true };
}

/** Whether a remote path belongs to the embedded (knowledge-only) surface. */
export function isEmbeddedRouteAdmitted(path: string): boolean {
  return ADMITTED_ROUTE_PREFIXES.some((prefix) => path === prefix || path.startsWith(`${prefix}/`));
}

/** Normalize arbitrary host input into a remote route (fail closed). */
export function normalizeEmbeddedRoute(route: unknown): string {
  if (typeof route !== 'string' || route.length === 0) return EMBEDDED_DEFAULT_ROUTE;
  // Strip any query string before validating the path grammar.
  const [path] = route.split('?');
  const withSlash = path.startsWith('/') ? path : `/${path}`;
  if (!ROUTE_RE.test(withSlash)) return EMBEDDED_DEFAULT_ROUTE;
  const trimmed = withSlash.length > 1 ? withSlash.replace(/\/+$/, '') : withSlash;
  if (trimmed === '' || trimmed === '/') return EMBEDDED_DEFAULT_ROUTE;
  if (!isEmbeddedRouteAdmitted(trimmed)) return EMBEDDED_DEFAULT_ROUTE;
  return trimmed;
}

/** Map a host-supplied path (with or without /platform) to a remote route. */
export function hostToRemoteRoute(path: unknown): string {
  if (typeof path !== 'string' || path.length === 0) return EMBEDDED_DEFAULT_ROUTE;
  const withSlash = path.startsWith('/') ? path : `/${path}`;
  const candidates = withSlash.startsWith('/platform/')
    ? [withSlash]
    : [`/platform${withSlash}`, withSlash];
  for (const candidate of candidates) {
    if (isEmbeddedRouteAdmitted(candidate)) return candidate;
  }
  return EMBEDDED_DEFAULT_ROUTE;
}

/**
 * Map a remote route back to the host-side URL suffix under
 * `/:workspaceSlug/knowledge/*`. Returns null when the remote path is not
 * admitted (the host keeps its current URL in that case).
 */
export function remoteToHostRoute(path: string): string | null {
  if (!isEmbeddedRouteAdmitted(path)) return null;
  return path.startsWith('/platform/') ? path.slice('/platform'.length) : path;
}

/** Default-deny capability evaluation for the embedded surface. */
export function isEmbeddedCapabilityAdmitted(
  capabilities: readonly string[] | undefined,
  key: EmbeddedCapabilityKey,
): boolean {
  if (!Array.isArray(capabilities)) return false;
  return capabilities.includes(key);
}

/** Map a host theme token onto the remote theme vocabulary. */
export function normalizeEmbeddedTheme(theme: unknown): 'light' | 'dark' {
  return theme === 'dark' ? 'dark' : 'light';
}
