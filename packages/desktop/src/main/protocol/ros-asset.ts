import { protocol } from 'electron'

import { ROS_ASSET_SCHEME, resolveRosAsset } from './ros-asset-core'

export * from './ros-asset-core'

/** Privilege record used by the one pre-ready registration in main. */
export function rosAssetSchemePrivileges(): Array<{
  scheme: string
  privileges: {
    standard: true
    secure: true
    supportFetchAPI: true
    stream: true
    corsEnabled: true
    bypassCSP: boolean
  }
}> {
  return [
    {
      scheme: ROS_ASSET_SCHEME,
      privileges: {
        standard: true,
        secure: true,
        supportFetchAPI: true,
        stream: true,
        corsEnabled: true,
        bypassCSP: false
      }
    }
  ]
}

/** Must run before app.whenReady(). Kept for callers that only need ros-asset. */
export function registerRosAssetSchemePrivileges(): void {
  protocol.registerSchemesAsPrivileged(rosAssetSchemePrivileges())
}

/** Installs the protocol handler; safe to call once after app.whenReady(). */
export function installRosAssetProtocol(getRoot: () => string | null): void {
  protocol.handle(ROS_ASSET_SCHEME, async (request) => {
    const result = await resolveRosAsset(getRoot(), request.url, request.headers.get('range'), request.method)
    // The resolver owns the exact byte window; copy it into a plain ArrayBuffer
    // so the BodyInit typing is satisfied without a Blob round-trip.
    const body =
      result.body === null
        ? null
        : (result.body.buffer.slice(
            result.body.byteOffset,
            result.body.byteOffset + result.body.byteLength
          ) as ArrayBuffer)
    return new Response(body, { status: result.status, headers: result.headers })
  })
}
