#!/usr/bin/env node
import { closeSync, copyFileSync, existsSync, mkdirSync, openSync, readSync, readdirSync, statSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'

const [directory, version, platform, arch] = process.argv.slice(2)
if (!directory || !version || !platform) {
  throw new Error('Usage: node validate-release-assets.mjs OUTPUT_DIRECTORY VERSION win|mac|linux|all [x64|arm64]')
}
const files = {
  'win-x64': [
    `ResearchOS-${version}-win-x64-setup.exe`,
    `ResearchOS-${version}-win-x64-portable.exe`
  ],
  'mac-x64': [
    `ResearchOS-${version}-mac-x64.dmg`,
    `ResearchOS-${version}-mac-x64.zip`
  ],
  'mac-arm64': [
    `ResearchOS-${version}-mac-arm64.dmg`,
    `ResearchOS-${version}-mac-arm64.zip`
  ],
  // electron-builder uses Debian's amd64 convention and x86_64 for RPM/AppImage.
  'linux-x64': [
    `ResearchOS-${version}-linux-amd64.deb`,
    `ResearchOS-${version}-linux-x86_64.rpm`,
    `ResearchOS-${version}-linux-x86_64.AppImage`
  ]
}
const expected = platform === 'all'
  ? Object.values(files).flat()
  : files[`${platform}-${arch}`]
if (!expected) throw new Error(`Unsupported platform/arch: ${platform}-${arch}`)
const target = resolve(directory)
const actual = readdirSync(target).filter(name => /^ResearchOS-/.test(name))
for (const file of expected) {
  const path = join(target, file)
  if (!existsSync(path)) throw new Error(`Missing distribution: ${path}`)
  const stats = statSync(path)
  if (!stats.isFile() || stats.size < 1_000_000) throw new Error(`Empty or unusually small distribution: ${path}`)
  const prefix = Buffer.alloc(8)
  const fd = openSync(path, 'r')
  try { readSync(fd, prefix, 0, prefix.length, 0) } finally { closeSync(fd) }
  const extension = file.slice(file.lastIndexOf('.')).toLowerCase()
  const matches = extension === '.exe' ? prefix.subarray(0, 2).toString() === 'MZ'
    : extension === '.zip' ? prefix.subarray(0, 2).toString() === 'PK'
    : extension === '.deb' ? prefix.toString() === '!<arch>\n'
    : extension === '.rpm' ? prefix.subarray(0, 4).toString('hex') === 'edabeedb'
    : extension === '.appimage' ? prefix.subarray(0, 4).toString('hex') === '7f454c46'
    : true // DMG verification requires a separate macOS tool/runtime check.
  if (!matches) throw new Error(`Unexpected file header: ${path}`)
  console.log(`OK ${file} (${(stats.size / 1024 / 1024).toFixed(1)} MiB)`)
}
if (platform === 'all') {
  const unexpected = actual.filter(file => !expected.includes(file))
  if (unexpected.length || actual.length !== 9) throw new Error(`Expected exactly nine release files; got ${actual.length}, unexpected: ${unexpected}`)
} else {
  const stage = resolve('release-assets')
  mkdirSync(stage, { recursive: true })
  for (const file of expected) copyFileSync(join(target, file), join(stage, basename(file)))
  console.log(`Staged ${expected.length} distribution files in ${stage}`)
}
