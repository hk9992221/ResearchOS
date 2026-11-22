import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { defineConfig } from 'vitest/config'

// Vitest resolves workspace packages to source so domain workers' tests never
// depend on a fresh dist build. Production builds keep using package exports.
const sharedSrcDirectory = fileURLToPath(new URL('./packages/shared/src', import.meta.url))
const coreSrcDirectory = fileURLToPath(new URL('./packages/core/src', import.meta.url))

export default defineConfig({
  resolve: {
    alias: [
      { find: /^@researchos\/shared$/, replacement: join(sharedSrcDirectory, 'index.ts') },
      { find: /^@researchos\/shared\/(.*)$/, replacement: join(sharedSrcDirectory, '$1') },
      { find: /^@researchos\/core$/, replacement: join(coreSrcDirectory, 'index.ts') },
      { find: /^@researchos\/core\/(.*)$/, replacement: join(coreSrcDirectory, '$1') }
    ]
  },
  test: {
    projects: [
      {
        extends: true,
        test: {
          name: 'node',
          include: [
            'packages/shared/src/**/*.test.ts',
            'packages/shared/test/**/*.test.ts',
            'packages/core/**/*.test.ts',
            'packages/desktop/src/main/**/*.test.ts',
            'packages/desktop/src/preload/**/*.test.ts',
            'scripts/**/*.test.ts'
          ],
          environment: 'node',
          testTimeout: 20_000
        }
      },
      {
        extends: true,
        test: {
          name: 'renderer',
          include: ['packages/desktop/src/renderer/**/*.test.ts', 'packages/desktop/src/renderer/**/*.test.tsx'],
          environment: 'jsdom',
          testTimeout: 20_000
        }
      }
    ]
  }
})