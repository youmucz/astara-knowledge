/**
 * CSS scoping tests for the embedded build: every rule lands below the
 * versioned root attribute and root-ish selectors are rewritten, not
 * duplicated (task 1.3 — style isolation).
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { scopeSelectorPart, EMBEDDED_ROOT_SELECTOR as ROOT } from './embedded-css-scope'

test('plain class selectors are prefixed below the root', () => {
  assert.equal(scopeSelectorPart('.t-button'), `${ROOT} .t-button`)
})

test('compound and descendant selectors keep their structure', () => {
  assert.equal(scopeSelectorPart('.a > .b'), `${ROOT} .a > .b`)
  assert.equal(scopeSelectorPart('.a:hover .b'), `${ROOT} .a:hover .b`)
})

test(':root selectors become the embedded root itself', () => {
  assert.equal(scopeSelectorPart(':root'), ROOT)
  assert.equal(scopeSelectorPart(':root:root'), ROOT)
  assert.equal(scopeSelectorPart(':root .t-foo'), `${ROOT} .t-foo`)
})

test('theme-mode compounds attach to the root (dark mode tokens)', () => {
  assert.equal(scopeSelectorPart(':root:root[theme-mode="dark"]'), `${ROOT}[theme-mode="dark"]`)
  assert.equal(scopeSelectorPart('[theme-mode="dark"] .t-x'), `${ROOT}[theme-mode="dark"] .t-x`)
})

test('html/body selectors collapse onto the root', () => {
  assert.equal(scopeSelectorPart('body'), ROOT)
  assert.equal(scopeSelectorPart('html'), ROOT)
  assert.equal(scopeSelectorPart('body .t-dialog'), `${ROOT} .t-dialog`)
  assert.equal(scopeSelectorPart('html.t-something .x'), `${ROOT}.t-something .x`)
})

test('bare universal selector styles both the root and its descendants', () => {
  assert.equal(scopeSelectorPart('*'), `${ROOT}, ${ROOT} *`)
  assert.equal(scopeSelectorPart('* .x'), `${ROOT} .x`)
})

test('scoping is idempotent', () => {
  const once = scopeSelectorPart('.t-button')
  assert.equal(scopeSelectorPart(once), once)
})

test('pseudo and attribute selectors survive', () => {
  assert.equal(scopeSelectorPart('input[type="text"]'), `${ROOT} input[type="text"]`)
  assert.equal(scopeSelectorPart('.a::before'), `${ROOT} .a::before`)
  assert.equal(scopeSelectorPart('.a:not(.b)'), `${ROOT} .a:not(.b)`)
})

test('empty and combinator-leading selectors are prefixed defensively', () => {
  assert.equal(scopeSelectorPart(''), '')
  assert.equal(scopeSelectorPart('> .foo'), `${ROOT} > .foo`)
})
