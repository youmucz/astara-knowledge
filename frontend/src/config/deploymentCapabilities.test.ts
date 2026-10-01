import assert from 'node:assert/strict'
import test from 'node:test'

import {
  SETTINGS_SECTION_CAPABILITY,
  isDeploymentCapabilitySupported,
  skillSettingsSupported,
  type DeploymentCapabilityMap,
} from './deploymentCapabilities'

test('capability filtering fails closed when discovery is missing or incomplete', () => {
  assert.equal(isDeploymentCapabilitySupported({}, 'organizations'), false)

  const capabilities: DeploymentCapabilityMap = {
    organizations: { supported: false, reason: 'not_supported_in_lite' },
    agents: { supported: true },
  }
  assert.equal(isDeploymentCapabilitySupported(capabilities, 'organizations'), false)
  assert.equal(isDeploymentCapabilitySupported(capabilities, 'agents'), true)
})

test('missing capabilities stay hidden in lite and standard deployments', () => {
  assert.equal(
    isDeploymentCapabilitySupported({}, 'organizations', { liteMode: true }),
    false,
  )
  assert.equal(
    isDeploymentCapabilitySupported({}, 'organizations', { edition: 'lite' }),
    false,
  )
  assert.equal(
    isDeploymentCapabilitySupported({}, 'agents', { liteMode: true }),
    false,
  )
})

test('only route-backed settings sections require deployment capabilities', () => {
  assert.equal(SETTINGS_SECTION_CAPABILITY.mcp, 'settings.mcp')
  assert.equal(SETTINGS_SECTION_CAPABILITY.storage, 'settings.storage')
  assert.equal(SETTINGS_SECTION_CAPABILITY.parser, undefined)
  assert.equal(SETTINGS_SECTION_CAPABILITY['runtime-queues'], undefined)
})

test('skill credentials follow the sandbox capability rather than a key of their own', () => {
  // The values are injected into a skill script's process, so a deployment with
  // no sandbox support has nowhere to put them and the page could only ever show
  // its empty state. Task 16 will gate this section with skillSettingsSupported.
  assert.equal(SETTINGS_SECTION_CAPABILITY.envvars, 'settings.sandbox')
})

test('the skill catalog follows the sandbox capability', () => {
  // Task 16 will gate this section with skillSettingsSupported.
  assert.equal(SETTINGS_SECTION_CAPABILITY.skills, 'settings.sandbox')
})

test('sandbox settings section requires the remote sandbox capability', () => {
  assert.equal(SETTINGS_SECTION_CAPABILITY.sandbox, 'settings.sandbox.remote')
})

test('host sandbox stays hidden unless the deployment explicitly enables it', () => {
  assert.equal(isDeploymentCapabilitySupported({}, 'settings.sandbox.host'), false)
  assert.equal(
    isDeploymentCapabilitySupported(
      { 'settings.sandbox.host': { supported: false, reason: 'platform_unsupported' } },
      'settings.sandbox.host',
    ),
    false,
  )
  assert.equal(
    isDeploymentCapabilitySupported(
      { 'settings.sandbox.host': { supported: true } },
      'settings.sandbox.host',
    ),
    true,
  )
})

test('docker sandbox stays hidden unless the deployment explicitly enables it', () => {
  assert.equal(isDeploymentCapabilitySupported({}, 'settings.sandbox.docker'), false)
  assert.equal(
    isDeploymentCapabilitySupported(
      { 'settings.sandbox.docker': { supported: false, reason: 'docker_backend_disabled' } },
      'settings.sandbox.docker',
    ),
    false,
  )
  assert.equal(
    isDeploymentCapabilitySupported(
      { 'settings.sandbox.docker': { supported: true } },
      'settings.sandbox.docker',
    ),
    true,
  )
})

test('remote sandbox capability fails closed', () => {
  assert.equal(isDeploymentCapabilitySupported({}, 'settings.sandbox.remote'), false)
  assert.equal(
    isDeploymentCapabilitySupported({ 'settings.sandbox.remote': { supported: true } }, 'settings.sandbox.remote'),
    true,
  )
})

test('skill settings need a remote sandbox or the host sandbox', () => {
  assert.equal(skillSettingsSupported({ 'settings.sandbox': { supported: true }, 'settings.sandbox.remote': { supported: true } }), true)
  assert.equal(skillSettingsSupported({ 'settings.sandbox': { supported: true }, 'settings.sandbox.host': { supported: true } }), true)
  assert.equal(skillSettingsSupported({ 'settings.sandbox': { supported: true } }), false)
  assert.equal(skillSettingsSupported({ 'settings.sandbox': { supported: false }, 'settings.sandbox.host': { supported: true } }), false)
})
