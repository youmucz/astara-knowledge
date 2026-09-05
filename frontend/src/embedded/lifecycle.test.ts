/**
 * Lifecycle tests: deterministic mount, in-place update, unmount, and
 * remount across Workspace changes (task 1.4).
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  createEmbeddedLifecycle,
  EmbeddedLifecycleError,
  type EmbeddedLifecycleApp,
  type EmbeddedLifecycleHandle,
} from './lifecycle'

interface FakeApp extends EmbeddedLifecycleApp {
  updates: Array<Record<string, unknown>>
  unmounted: boolean
  unmountCalls: number
}

function makeFakeApp(
  factoryImpl?: (container: unknown, props: Record<string, unknown>) => void | Promise<void>,
): { apps: FakeApp[]; handle: EmbeddedLifecycleHandle; errors: Array<{ code: string; message?: string }> } {
  const apps: FakeApp[] = []
  const errors: Array<{ code: string; message?: string }> = []
  const handle = createEmbeddedLifecycle({
    async createAppInstance(container, props) {
      await factoryImpl?.(container, props)
      const app: FakeApp = {
        updates: [],
        unmounted: false,
        unmountCalls: 0,
        update(next) {
          if (this.unmounted) throw new Error('update on unmounted app')
          this.updates.push(next)
        },
        async unmount() {
          this.unmountCalls += 1
          this.unmounted = true
        },
      }
      apps.push(app)
      return app
    },
    onError: (error) => errors.push(error),
  })
  return { apps, handle, errors }
}

const container = {} as HTMLElement

test('mount creates exactly one app and reaches the mounted state', async () => {
  const { apps, handle } = makeFakeApp()
  await handle.mount(container, { workspace: { id: 'ws-1' } })
  assert.equal(apps.length, 1)
  assert.equal(handle.state(), 'mounted')
})

test('double mount is rejected without touching the mounted app', async () => {
  const { apps, handle } = makeFakeApp()
  await handle.mount(container, { workspace: { id: 'ws-1' } })
  await assert.rejects(() => handle.mount(container, { workspace: { id: 'ws-2' } }), EmbeddedLifecycleError)
  assert.equal(apps.length, 1)
  assert.equal(handle.state(), 'mounted')
})

test('update while mounted applies props in place', async () => {
  const { apps, handle } = makeFakeApp()
  await handle.mount(container, { workspace: { id: 'ws-1' } })
  handle.update({ theme: 'dark' })
  assert.equal(handle.state(), 'mounted')
  assert.equal(apps[0].updates.length, 1)
  assert.deepEqual(apps[0].updates[0], { theme: 'dark' })
})

test('update before mount and after unmount is rejected and reported', async () => {
  const { handle, errors } = makeFakeApp()
  assert.throws(() => handle.update({ theme: 'dark' }), EmbeddedLifecycleError)
  assert.equal(errors[0].code, 'update_invalid_state')

  await handle.mount(container, {})
  await handle.unmount()
  assert.throws(() => handle.update({ theme: 'dark' }), EmbeddedLifecycleError)
  assert.equal(errors[1].code, 'update_invalid_state')
})

test('unmount resolves only after the app finished tearing down and is idempotent', async () => {
  const { apps, handle } = makeFakeApp()
  await handle.mount(container, {})
  await handle.unmount()
  assert.equal(handle.state(), 'unmounted')
  assert.equal(apps[0].unmounted, true)
  // Repeated unmount is a no-op — never a second teardown.
  await handle.unmount()
  assert.equal(apps[0].unmountCalls, 1)
})

test('unmount racing an in-flight mount still disposes the fresh app', async () => {
  let releaseFactory!: () => void
  const factoryDone = new Promise<void>((resolve) => {
    releaseFactory = resolve
  })
  const { apps, handle } = makeFakeApp(async () => {
    await factoryDone
  })
  const mounting = handle.mount(container, {})
  const unmounting = handle.unmount()
  releaseFactory()
  await Promise.all([mounting, unmounting])
  assert.equal(handle.state(), 'unmounted')
  assert.equal(apps.length, 1)
  assert.equal(apps[0].unmounted, true)
})

test('remount after unmount creates a fresh app with fresh state', async () => {
  const { apps, handle } = makeFakeApp()
  await handle.mount(container, { workspace: { id: 'ws-1' } })
  handle.update({ theme: 'dark' })
  await handle.unmount()

  // Workspace change: the host unmounts, then mounts again for ws-2.
  await handle.mount(container, { workspace: { id: 'ws-2' } })
  assert.equal(apps.length, 2)
  assert.notEqual(apps[0], apps[1])
  assert.equal(apps[0].unmounted, true)
  assert.equal(apps[1].unmounted, false)
  assert.equal(apps[1].updates.length, 0, 'remounted app must not inherit prior updates')
  assert.equal(handle.state(), 'mounted')
})

test('factory failure leaves the machine unmounted and propagates the error', async () => {
  let attempts = 0
  const { apps, handle } = makeFakeApp(async () => {
    attempts += 1
    if (attempts === 1) {
      throw new Error('bootstrap failed')
    }
  })
  await assert.rejects(() => handle.mount(container, {}), /bootstrap failed/)
  assert.equal(handle.state(), 'unmounted')
  assert.equal(apps.length, 0)
  // The machine stays usable after a failed bootstrap.
  await handle.mount(container, {})
  assert.equal(handle.state(), 'mounted')
  assert.equal(apps.length, 1)
})

test('unmount failures are reported but still settle the machine', async () => {
  const errors: Array<{ code: string }> = []
  const handle = createEmbeddedLifecycle({
    async createAppInstance() {
      return {
        update: () => {},
        unmount: async () => {
          throw new Error('teardown exploded')
        },
      }
    },
    onError: (error) => errors.push(error),
  })
  await handle.mount(container, {})
  await handle.unmount()
  assert.equal(handle.state(), 'unmounted')
  assert.equal(errors.length, 1)
  assert.equal(errors[0].code, 'unmount_failed')
})
