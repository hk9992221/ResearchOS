/// <reference types="vite/client" />

import type { ResearchOsBridge } from '@researchos/shared'

declare global {
  interface Window {
    /** Exposed by the preload script; absent in a plain browser or unit test. */
    researchos?: ResearchOsBridge
  }
}