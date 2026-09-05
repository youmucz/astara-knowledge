/**
 * Embedded (Plane-hosted) build of the Knowledge frontend.
 *
 * Produces a self-contained ESM microfrontend artifact:
 *
 *   dist-embedded/knowledge-embedded.js   — exports KNOWLEDGE_UI_CONTRACT_VERSION,
 *                                            mount(), update(), unmount()
 *   dist-embedded/knowledge-embedded.css  — every rule scoped under
 *                                            [data-weknora-embedded="v1"]
 *   dist-embedded/embedded-manifest.json  — version + contract + SRI digests
 *
 * Run with: npx vite build --config vite.config.embedded.ts
 */

import { fileURLToPath, URL } from 'node:url'
import { createHash } from 'node:crypto'
import { readFileSync, existsSync, writeFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { createRequire } from 'node:module'
import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import vueJsx from '@vitejs/plugin-vue-jsx'
import type { AtRule, Plugin as PostCSSPlugin, Rule } from 'postcss'
import { EMBEDDED_ROOT_SELECTOR, scopeSelectorPart } from './scripts/embedded-css-scope'
import {
  EMBEDDED_PORTAL_SELECTOR,
  KNOWLEDGE_UI_CONTRACT_VERSION,
} from './src/embedded/contract'

const __dirname = dirname(fileURLToPath(import.meta.url))
const require = createRequire(import.meta.url)
const pkg = require('./package.json') as { version?: string }

const OUT_DIR = 'dist-embedded'
const ENTRY_NAME = 'knowledge-embedded'

/* ------------------------------------------------------------------ */
/* CSS scoping: prefix every selector below the embedded root          */
/* ------------------------------------------------------------------ */

const scopeEmbeddedCssPlugin: PostCSSPlugin = {
  postcssPlugin: 'weknora-embedded-css-scope',
  Rule(rule: Rule) {
    const parent = rule.parent
    if (parent && parent.type === 'atrule' && (parent as AtRule).name === 'keyframes') return
    rule.selector = rule.selector
      .split(',')
      .map((part) => scopeSelectorPart(part, EMBEDDED_ROOT_SELECTOR))
      .join(',')
  },
}

/* ------------------------------------------------------------------ */
/* Teleport rewrite: body portals land inside the embedded portal root */
/* ------------------------------------------------------------------ */

function rewriteTeleportTargets(): Plugin {
  return {
    name: 'weknora-embedded-teleport-rewrite',
    enforce: 'post',
    transform(code, id) {
      if (!/\.(vue|js|ts|jsx|tsx|mjs)(\?.*)?$/.test(id)) return null
      if (!code.includes('"body"') && !code.includes("'body'")) return null
      const next = code
        .replace(/\bto:\s*"body"/g, `to: ${JSON.stringify(EMBEDDED_PORTAL_SELECTOR)}`)
        .replace(/\bto:\s*'body'/g, `to: ${JSON.stringify(EMBEDDED_PORTAL_SELECTOR)}`)
        .replace(/\battach:\s*"body"/g, `attach: ${JSON.stringify(EMBEDDED_PORTAL_SELECTOR)}`)
        .replace(/\battach:\s*'body'/g, `attach: ${JSON.stringify(EMBEDDED_PORTAL_SELECTOR)}`)
      if (next === code) return null
      return { code: next, map: null }
    },
  }
}

/* ------------------------------------------------------------------ */
/* Manifest emission with SRI digests                                  */
/* ------------------------------------------------------------------ */

function sha384(content: string): string {
  return `sha384-${createHash('sha384').update(content, 'utf8').digest('base64')}`
}

function emitEmbeddedManifest(): Plugin {
  return {
    name: 'weknora-embedded-manifest',
    apply: 'build',
    closeBundle() {
      const outDir = resolve(__dirname, OUT_DIR)
      const files: Record<string, { file: string; integrity: string }> = {}
      const entry = resolve(outDir, `${ENTRY_NAME}.js`)
      if (existsSync(entry)) {
        files.entry = {
          file: `${ENTRY_NAME}.js`,
          integrity: sha384(readFileSync(entry, 'utf8')),
        }
      }
      const css = resolve(outDir, `${ENTRY_NAME}.css`)
      if (existsSync(css)) {
        files.styles = {
          file: `${ENTRY_NAME}.css`,
          integrity: sha384(readFileSync(css, 'utf8')),
        }
      }
      const manifest = {
        schema: 'weknora-embedded-manifest',
        schemaVersion: 1,
        version: pkg.version ?? 'unknown',
        contractVersion: KNOWLEDGE_UI_CONTRACT_VERSION,
        rootAttribute: 'data-weknora-embedded',
        files,
      }
      writeFileSync(resolve(outDir, 'embedded-manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`, 'utf8')
    },
  }
}

export default defineConfig({
  // The artifact is consumed by the Plane host, not served as a site: no
  // public/ assets (config.js, widget loader, icons) belong in it.
  publicDir: false,
  define: {
    __FRONTEND_VERSION__: JSON.stringify(pkg.version ?? 'unknown'),
    __FRONTEND_COMMIT__: JSON.stringify(process.env.VITE_FRONTEND_COMMIT ?? 'embedded'),
  },
  plugins: [vue(), vueJsx(), rewriteTeleportTargets(), emitEmbeddedManifest()],
  css: {
    postcss: {
      plugins: [scopeEmbeddedCssPlugin],
    },
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    outDir: OUT_DIR,
    emptyOutDir: true,
    cssCodeSplit: false,
    lib: {
      entry: resolve(__dirname, 'src/embedded/embedded-main.ts'),
      formats: ['es'],
      name: 'WeKnoraEmbedded',
      fileName: () => `${ENTRY_NAME}.js`,
      cssFileName: ENTRY_NAME,
    },
    rollupOptions: {
      output: {
        // Single self-contained artifact: the host never shares Vue chunks
        // with the remote, and the whole surface is covered by one SRI
        // digest. Dynamic imports (route components) are inlined.
        inlineDynamicImports: true,
        assetFileNames: `${ENTRY_NAME}.[ext]`,
      },
    },
  },
})
