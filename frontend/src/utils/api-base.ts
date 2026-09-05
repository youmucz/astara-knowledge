// Embedded Mode: when the knowledge surface is mounted inside Plane, every
// request is routed through the same-origin reserved proxy prefix
// (/api/knowledge/...) and authenticated by the short-lived HttpOnly
// embedded-session cookie instead of a localStorage JWT.
let apiBaseOverride: string | null = null;

export function setApiBaseOverride(override: string | null): void {
  apiBaseOverride = override === '' ? null : override;
}

export function getApiBaseOverride(): string | null {
  return apiBaseOverride;
}

export function getApiBaseUrl(): string {
  if (apiBaseOverride !== null) {
    return apiBaseOverride.replace(/\/+$/, '');
  }
  // LocalHub plugin patch (2026-04-29): respect vite's BASE_URL so that
  // axios calls work at `/app/weknora/` (LocalHub reverse proxy). Without
  // this · axios hits `/api/v1/...` at LocalHub root · gets 404 "Cannot
  // POST". Strip trailing slash so axios doesn't produce `/app/weknora//api/v1/...`.
  // See: plugins/weknora/patches/api-base-baseurl.patch
  const base = (import.meta.env.BASE_URL || '/').replace(/\/+$/, '');
  return base;
}
