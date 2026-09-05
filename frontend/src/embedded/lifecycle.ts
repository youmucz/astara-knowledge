/**
 * Deterministic mount/update/unmount lifecycle for the embedded surface.
 *
 * The state machine is intentionally decoupled from Vue so its contract can
 * be unit-tested without a DOM: the caller injects an app factory
 * (`createAppInstance`) plus an error sink, and the machine guarantees:
 *
 *  - at most one mounted app per machine (`mount` while mounted rejects);
 *  - `update` is only valid while mounted (fail closed otherwise);
 *  - `unmount` resolves only after the underlying app finished tearing down
 *    and is idempotent;
 *  - `unmount` racing an in-flight `mount` still disposes the freshly created
 *    app before resolving;
 *  - a new `mount` after `unmount` (remount, e.g. after a Workspace switch)
 *    creates a fresh app and never reuses torn-down state.
 */

export type EmbeddedLifecycleState =
  | 'idle'
  | 'mounting'
  | 'mounted'
  | 'unmounting'
  | 'unmounted';

export interface EmbeddedLifecycleApp {
  /** Apply new props in place. */
  update(props: Record<string, unknown>): void;
  /** Tear down; resolve when safe to detach the container. */
  unmount(): Promise<void>;
}

export interface EmbeddedLifecycleOptions {
  /** Creates the actual app (Vue wiring lives in embedded-main.ts). */
  createAppInstance(
    container: HTMLElement,
    props: Record<string, unknown>,
  ): Promise<EmbeddedLifecycleApp>;
  /** Error sink; defaults to console.error. */
  onError?(error: { code: string; message?: string }): void;
}

export interface EmbeddedLifecycleHandle {
  readonly state: () => EmbeddedLifecycleState;
  mount(container: HTMLElement, props: Record<string, unknown>): Promise<void>;
  update(props: Record<string, unknown>): void;
  unmount(): Promise<void>;
}

export class EmbeddedLifecycleError extends Error {
  constructor(
    readonly code: string,
    message?: string,
  ) {
    super(message ?? code);
    this.name = 'EmbeddedLifecycleError';
  }
}

export function createEmbeddedLifecycle(options: EmbeddedLifecycleOptions): EmbeddedLifecycleHandle {
  let state: EmbeddedLifecycleState = 'idle';
  let app: EmbeddedLifecycleApp | null = null;
  let unmountRequested = false;
  let pendingUnmountResolve: (() => void) | null = null;
  let teardown: Promise<void> | null = null;

  const reportError = (code: string, message?: string) => {
    try {
      (options.onError ?? ((e) => console.error(`[embedded] ${e.code}`, e.message)))({ code, message });
    } catch {
      /* the error sink must never break the lifecycle */
    }
  };

  const finishUnmount = () => {
    state = 'unmounted';
    const resolve = pendingUnmountResolve;
    pendingUnmountResolve = null;
    resolve?.();
  };

  return {
    state: () => state,

    async mount(container, props) {
      if (state !== 'idle' && state !== 'unmounted') {
        throw new EmbeddedLifecycleError('mount_conflict', 'an embedded app is already mounted');
      }
      state = 'mounting';
      unmountRequested = false;
      try {
        const created = await options.createAppInstance(container, props);
        if (unmountRequested) {
          // unmount() raced the factory: dispose the fresh app, keep the
          // machine unmounted, then release the waiting unmount() caller.
          app = null;
          try {
            await created.unmount();
          } catch (error) {
            reportError('unmount_failed', String(error));
          }
          finishUnmount();
          return;
        }
        app = created;
        state = 'mounted';
      } catch (error) {
        app = null;
        finishUnmount();
        throw error;
      }
    },

    update(props) {
      if (state !== 'mounted' || app === null) {
        reportError('update_invalid_state', `update() called while ${state}`);
        throw new EmbeddedLifecycleError('update_invalid_state', `update() called while ${state}`);
      }
      app.update(props);
    },

    unmount() {
      if (state === 'unmounted' || state === 'idle') {
        // Idempotent: repeated unmount is a no-op.
        return Promise.resolve();
      }
      if (teardown) {
        return teardown;
      }
      if (state === 'mounting') {
        // Signal the in-flight mount; it disposes the fresh app itself.
        unmountRequested = true;
        teardown = new Promise<void>((resolve) => {
          pendingUnmountResolve = resolve;
        });
        return teardown;
      }
      const current = app;
      app = null;
      state = 'unmounting';
      teardown = (async () => {
        try {
          if (current) await current.unmount();
        } catch (error) {
          reportError('unmount_failed', String(error));
        } finally {
          finishUnmount();
          teardown = null;
        }
      })();
      return teardown;
    },
  };
}
