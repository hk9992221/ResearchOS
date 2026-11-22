import { cpSync, createReadStream, existsSync, statSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'

import react from '@vitejs/plugin-react'
import { defineConfig, externalizeDepsPlugin } from 'electron-vite'
import type { Plugin } from 'vite'

// Preload and renderer consume the shared contract from source: it is pure
// TypeScript with zero runtime dependencies, and this avoids CJS interop in the
// sandboxed preload bundle.
const sharedSource = resolve('../shared/src/index.ts')

const PDFJS_ASSET_KINDS = ['wasm', 'cmaps'] as const
const PDFJS_ASSET_PREFIX = '/pdfjs/'
const PDFJS_ASSET_TYPES: Readonly<Record<string, string>> = {
  '.wasm': 'application/wasm',
  '.js': 'text/javascript',
  '.bcmap': 'application/octet-stream'
}

/**
 * pdf.js 6 decodes JBIG2/JPEG 2000 scans with WebAssembly modules and loads
 * CMaps for CJK text; both ship as static files next to the package. The
 * renderer fetches them under `pdfjs/<kind>/<file>` (see PdfCanvas), so serve
 * that prefix during dev and copy the same tree into the renderer output on
 * build — otherwise scanned backdrops are silently skipped.
 */
function pdfjsAssets(): Plugin {
  const sourceRoot = resolve('node_modules/pdfjs-dist')

  return {
    name: 'researchos:pdfjs-assets',

    configureServer(server) {
      server.middlewares.use((request, response, next) => {
        const url = request.url ?? ''
        if (!url.startsWith(PDFJS_ASSET_PREFIX)) {
          next()
          return
        }
        let pathname: string
        try {
          pathname = decodeURIComponent(url.slice(PDFJS_ASSET_PREFIX.length).split(/[?#]/u)[0] ?? '')
        } catch {
          next()
          return
        }
        const segments = pathname.split('/')
        const kind = segments.shift() ?? ''
        if (!(PDFJS_ASSET_KINDS as readonly string[]).includes(kind) || segments.length === 0) {
          next()
          return
        }
        if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
          next()
          return
        }
        const directory = join(sourceRoot, kind)
        const absolute = join(directory, ...segments)
        if (!absolute.startsWith(`${directory}${sep}`) || !existsSync(absolute)) {
          next()
          return
        }
        const stats = statSync(absolute)
        if (!stats.isFile()) {
          next()
          return
        }
        response.setHeader(
          'content-type',
          PDFJS_ASSET_TYPES[extname(absolute).toLowerCase()] ?? 'application/octet-stream'
        )
        response.setHeader('content-length', stats.size)
        if (request.method === 'HEAD') {
          response.end()
          return
        }
        createReadStream(absolute).pipe(response)
      })
    },

    writeBundle(options) {
      if (options.dir === undefined) return
      for (const kind of PDFJS_ASSET_KINDS) {
        cpSync(join(sourceRoot, kind), join(options.dir, 'pdfjs', kind), { recursive: true })
      }
    }
  }
}

export default defineConfig({
  // Main runs in Node: keep workspace packages external so their CJS builds and
  // native dependencies resolve through pnpm's per-package node_modules.
  main: {
    plugins: [externalizeDepsPlugin()]
  },
  preload: {
    plugins: [externalizeDepsPlugin({ exclude: ['@researchos/shared'] })],
    resolve: { alias: { '@researchos/shared': sharedSource } }
  },
  renderer: {
    resolve: { alias: { '@researchos/shared': sharedSource } },
    plugins: [react(), pdfjsAssets()]
  }
})