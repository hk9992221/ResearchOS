import { lstatSync, readFileSync, realpathSync } from 'node:fs'
import * as path from 'node:path'

import { PLUGIN_ID_PATTERN } from '@researchos/core/modules/plugins/index'

/**
 * `plugin://<id>/...` protocol (plan §3.7). Assets are served only from the
 * plugin's own folder; every path is decoded, segment-checked, realpath-checked
 * and refused when it leaves the root or is a symlink. `file://` is never used
 * for plugin content.
 */

export const PLUGIN_SCHEME = 'plugin'
export const PLUGIN_CSP =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
  "img-src 'self' data: blob:; font-src 'self' data:; connect-src 'none'; " +
  "object-src 'none'; base-uri 'none'; form-action 'none'"

const HOST_PREFIX = '__host__'
const MIME_TYPES: Readonly<Record<string, string>> = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.txt': 'text/plain; charset=utf-8',
  '.md': 'text/plain; charset=utf-8'
}

export interface PluginProtocolOptions {
  /** Workspace `<root>/plugins`. */
  pluginsRoot: string
}

export interface ResolvedPluginAsset {
  kind: 'file' | 'generated'
  pluginId: string
  mime: string
  body: Buffer | string
  headers: Record<string, string>
}

export class PluginProtocolError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'PluginProtocolError'
    this.status = status
  }
}

function decodeAssetPath(pathname: string): string {
  if (pathname.startsWith('//') || pathname.includes('//')) {
    throw new PluginProtocolError(403, 'plugin asset path has an empty segment')
  }
  let decoded: string
  try {
    decoded = decodeURIComponent(pathname)
  } catch {
    throw new PluginProtocolError(400, 'invalid URL encoding')
  }
  if (decoded.includes('\\') || decoded.includes('\0')) {
    throw new PluginProtocolError(400, 'invalid plugin asset path')
  }
  const relative = decoded.replace(/^\/+/, '')
  if (relative === '') throw new PluginProtocolError(404, 'plugin asset path is empty')
  const segments = relative.split('/')
  if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
    throw new PluginProtocolError(403, 'plugin asset path escapes the plugin root')
  }
  return relative
}

function isInside(root: string, candidate: string): boolean {
  const relative = path.relative(root, candidate)
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative))
}

function readPluginFile(pluginsRoot: string, pluginId: string, relative: string): Buffer {
  const root = path.join(pluginsRoot, pluginId)
  let realRoot: string
  try {
    realRoot = realpathSync(root)
  } catch {
    throw new PluginProtocolError(404, `plugin ${pluginId} is not installed`)
  }
  const target = path.join(realRoot, ...relative.split('/'))
  if (!isInside(realRoot, target)) throw new PluginProtocolError(403, 'plugin asset escapes the plugin root')
  let info
  try {
    info = lstatSync(target)
  } catch {
    throw new PluginProtocolError(404, `plugin asset not found: ${relative}`)
  }
  if (info.isSymbolicLink() || !info.isFile()) {
    throw new PluginProtocolError(403, 'plugin assets must be regular files')
  }
  let realTarget: string
  try {
    realTarget = realpathSync(target)
  } catch {
    throw new PluginProtocolError(404, `plugin asset not found: ${relative}`)
  }
  if (!isInside(realRoot, realTarget)) throw new PluginProtocolError(403, 'plugin asset escapes the plugin root')
  if (info.size > 32 * 1024 * 1024) throw new PluginProtocolError(413, 'plugin asset is too large')
  return readFileSync(realTarget)
}

/** Pure resolver used by the protocol handler and the tests. */
export function resolvePluginAsset(
  url: string,
  pluginIdOverride: string | undefined,
  options: PluginProtocolOptions
): ResolvedPluginAsset {
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    throw new PluginProtocolError(400, 'invalid plugin URL')
  }
  if (parsed.protocol !== `${PLUGIN_SCHEME}:`) throw new PluginProtocolError(400, 'not a plugin URL')
  const pluginId = pluginIdOverride ?? parsed.hostname
  if (!PLUGIN_ID_PATTERN.test(pluginId)) throw new PluginProtocolError(400, 'invalid plugin id')
  if (parsed.username !== '' || parsed.password !== '' || parsed.port !== '') {
    throw new PluginProtocolError(400, 'plugin URLs carry no credentials or port')
  }
  const relative = decodeAssetPath(parsed.pathname)
  const headers = {
    'Content-Security-Policy': PLUGIN_CSP,
    'X-Content-Type-Options': 'nosniff',
    'Cache-Control': 'no-store'
  }
  if (relative === `${HOST_PREFIX}/iframe.html`) {
    const entry = parsed.searchParams.get('entry') ?? ''
    if (entry === '') throw new PluginProtocolError(400, 'iframe entry is required')
    const entryPath = decodeAssetPath(`/${entry}`)
    readPluginFile(options.pluginsRoot, pluginId, entryPath)
    return {
      kind: 'generated',
      pluginId,
      mime: MIME_TYPES['.html'] ?? 'text/html',
      body: buildPluginFrameHtml(pluginId, entryPath),
      headers
    }
  }
  if (relative === `${HOST_PREFIX}/bridge.js`) {
    return {
      kind: 'generated',
      pluginId,
      mime: MIME_TYPES['.js'] ?? 'text/javascript',
      body: PLUGIN_IFRAME_BRIDGE_SCRIPT,
      headers
    }
  }
  if (relative.startsWith(`${HOST_PREFIX}/`)) {
    throw new PluginProtocolError(404, 'unknown host asset')
  }
  const body = readPluginFile(options.pluginsRoot, pluginId, relative)
  const extension = path.posix.extname(relative).toLowerCase()
  return {
    kind: 'file',
    pluginId,
    mime: MIME_TYPES[extension] ?? 'application/octet-stream',
    body,
    headers
  }
}

/** Builds the sandboxed iframe document that hosts `renderer.js`. */
export function buildPluginFrameHtml(pluginId: string, entry: string): string {
  const safeEntry = entry
    .split('/')
    .map((segment) => encodeURIComponent(segment))
    .join('/')
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="${PLUGIN_CSP.replaceAll('"', '&quot;')}">
<style>
  :root { color-scheme: light dark; font-family: system-ui, sans-serif; }
  html, body { margin: 0; padding: 0; background: transparent; color: inherit; }
  #plugin-root { padding: 8px; overflow: auto; }
</style>
</head>
<body>
<div id="plugin-root"></div>
<script src="/__host__/bridge.js"></script>
<script type="module" src="/${safeEntry}"></script>
</body>
</html>`
}

/** The iframe-side bridge; it never sees Node, host DOM or `window.researchos`. */
export const PLUGIN_IFRAME_BRIDGE_SCRIPT = `(() => {
  const pending = new Map();
  const listeners = new Set();
  let port = null;
  let sequence = 0;
  function sendRequest(method, params) {
    if (!port) return Promise.reject(new Error('plugin bridge is not connected'));
    if (typeof method !== 'string' || method === '') return Promise.reject(new TypeError('method must be a string'));
    sequence += 1;
    const requestId = 'frame-' + sequence;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(requestId);
        reject(new Error('plugin RPC timed out'));
      }, 15000);
      pending.set(requestId, { resolve, reject, timer });
      port.postMessage({ type: 'request', requestId, method, params });
    });
  }
  window.pluginBridge = Object.freeze({
    request: sendRequest,
    onEvent(handler) {
      if (typeof handler !== 'function') throw new TypeError('onEvent requires a function');
      listeners.add(handler);
      return () => listeners.delete(handler);
    },
    resize(width, height) {
      if (!Number.isFinite(width) || !Number.isFinite(height)) throw new TypeError('resize requires finite numbers');
      if (port) port.postMessage({ type: 'resize', width, height });
    }
  });
  window.addEventListener('message', (event) => {
    if (event.source !== window.parent) return;
    if (!event.data || event.data.type !== 'plugin:init') return;
    const next = event.ports && event.ports[0];
    if (!next) return;
    port = next;
    port.start();
    port.onmessage = (messageEvent) => {
      const message = messageEvent.data;
      if (!message) return;
      if (message.type === 'response') {
        const entry = pending.get(message.requestId);
        if (!entry) return;
        pending.delete(message.requestId);
        clearTimeout(entry.timer);
        if (message.ok) entry.resolve(message.result);
        else entry.reject(new Error(message.error && message.error.message ? message.error.message : 'plugin RPC failed'));
        return;
      }
      if (message.type === 'event' && message.event && typeof message.event.type === 'string') {
        for (const listener of listeners) {
          try { listener(message.event); } catch (error) { console.error(error); }
        }
      }
    };
    if (event.data.locale) document.documentElement.lang = String(event.data.locale);
    const theme = event.data.theme;
    if (theme && typeof theme === 'object') {
      if (theme.mode === 'dark' || theme.mode === 'light') document.documentElement.dataset.theme = theme.mode;
      if (theme.tokens && typeof theme.tokens === 'object') {
        for (const [key, value] of Object.entries(theme.tokens)) {
          if (key.startsWith('--') && typeof value === 'string') document.documentElement.style.setProperty(key, value);
        }
      }
    }
    window.parent.postMessage({ type: 'plugin:ready', pluginId: event.data.pluginId }, '*');
  });
})();
`

export interface ProtocolRegistrar {
  handle(scheme: string, handler: (request: Request) => Response | Promise<Response>): void
}

/** Registers the protocol on Electron's `protocol` module after app ready. */
export function registerPluginProtocol(registrar: ProtocolRegistrar, options: PluginProtocolOptions): void {
  registrar.handle(PLUGIN_SCHEME, async (request) => {
    try {
      const asset = resolvePluginAsset(request.url, undefined, options)
      const body = typeof asset.body === 'string' ? asset.body : new Uint8Array(asset.body)
      return new Response(body, {
        status: 200,
        headers: {
          ...asset.headers,
          'Content-Type': asset.mime
        }
      })
    } catch (error) {
      const status = error instanceof PluginProtocolError ? error.status : 500
      const message = error instanceof Error ? error.message : 'plugin protocol error'
      return new Response(message, {
        status,
        headers: {
          'Content-Type': 'text/plain; charset=utf-8',
          'Cache-Control': 'no-store',
          'X-Content-Type-Options': 'nosniff'
        }
      })
    }
  })
}

/** URL the renderer iframe uses for a plugin's custom panel. */
export function pluginFrameUrl(pluginId: string, rendererEntry: string): string {
  return `${PLUGIN_SCHEME}://${pluginId}/${HOST_PREFIX}/iframe.html?entry=${encodeURIComponent(rendererEntry)}`
}