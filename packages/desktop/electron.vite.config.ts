import { resolve } from 'node:path'

import react from '@vitejs/plugin-react'
import { defineConfig, externalizeDepsPlugin } from 'electron-vite'

// Preload and renderer consume the shared contract from source: it is pure
// TypeScript with zero runtime dependencies, and this avoids CJS interop in the
// sandboxed preload bundle.
const sharedSource = resolve('../shared/src/index.ts')

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
    plugins: [react()]
  }
})