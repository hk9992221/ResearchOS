import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import * as path from 'node:path'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { PLUGIN_CSP, resolvePluginAsset } from './protocol'

/**
 * The panel iframe runs with `sandbox="allow-scripts"` (opaque origin), so
 * `type="module"` scripts are fetched cross-origin. A response without
 * `Access-Control-Allow-Origin` silently blocks the module and the panel
 * renders blank — every plugin asset must therefore carry a CORS header.
 */

const pluginsRoot = path.join(process.cwd(), '.tmp-protocol-test-plugins')
const pluginId = 'com.example.panel-test'

beforeAll(() => {
  const dir = path.join(pluginsRoot, pluginId)
  mkdirSync(dir, { recursive: true })
  writeFileSync(path.join(dir, 'renderer.js'), 'export {}', 'utf8')
  writeFileSync(path.join(dir, 'manifest.json'), '{}', 'utf8')
})

afterAll(() => {
  rmSync(pluginsRoot, { recursive: true, force: true })
})

describe('resolvePluginAsset CORS headers', () => {
  it('allows cross-origin module fetches for plugin files', () => {
    const asset = resolvePluginAsset(`plugin://${pluginId}/renderer.js`, undefined, { pluginsRoot })
    expect(asset.headers['Access-Control-Allow-Origin']).toBe('*')
    expect(asset.headers['Content-Security-Policy']).toBe(PLUGIN_CSP)
  })

  it('allows cross-origin module fetches for the generated iframe document', () => {
    const asset = resolvePluginAsset(
      `plugin://${pluginId}/__host__/iframe.html?entry=renderer.js`,
      undefined,
      { pluginsRoot }
    )
    expect(asset.kind).toBe('generated')
    expect(asset.headers['Access-Control-Allow-Origin']).toBe('*')
  })
})
