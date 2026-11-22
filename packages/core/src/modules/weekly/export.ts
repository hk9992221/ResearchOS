import { accessSync, constants, existsSync, mkdirSync, renameSync, statSync, unlinkSync, writeFileSync } from 'node:fs'
import * as path from 'node:path'

import { AppError, AppErrorCode } from '@researchos/shared'

import type { WeeklyContext, WeeklyFileSystem } from './context'
import { projectInfoFor } from './generate'
import { readWeeklySettings, requireReport, writeActor } from './reports'
import { periodEndDate } from './template'
import type { ExportInput, ExportResult, WeeklyReport } from './types'

export const MARKDOWN_EXT = '.md'
export const HTML_EXT = '.html'

/** Default `node:fs` adapter; tests inject a failing one to prove error codes. */
export const NODE_FILE_SYSTEM: WeeklyFileSystem = {
  stat: (target) => statSync(target),
  access: (target, mode) => accessSync(target, mode),
  mkdir: (target, options) => mkdirSync(target, options),
  writeFile: (target, data, options) => writeFileSync(target, data, { flag: options.exclusive ? 'wx' : 'w' }),
  rename: (from, to) => renameSync(from, to),
  unlink: (target) => unlinkSync(target),
  exists: (target) => existsSync(target)
}

let tempCounter = 0

/**
 * Exports one report as Markdown or HTML.
 *
 * The destination is an absolute OS path and may lie outside the workspace
 * (defect 8): the user chose it. Only the project directory is refused. Every
 * filesystem failure maps to `INVALID_ARGUMENT`, `EXTERNAL_UNAVAILABLE` or
 * `CONFLICT`; this function never returns `INTERNAL`.
 */
export function exportReport(context: WeeklyContext, input: ExportInput): ExportResult {
  const format = normalizeFormat(input.format)
  writeActor(input.actor)
  const settings = readWeeklySettings(context)
  const report = requireReport(context.handle.db, input.reportId, settings.timeZone)
  const info = projectInfoFor(context)
  const now = context.clock.now()
  const frontMatter = input.frontMatter !== false

  const ext = format === 'html' ? HTML_EXT : MARKDOWN_EXT
  const payload =
    format === 'html'
      ? renderHtml(report, { name: info.name }, frontMatter, now)
      : Buffer.from(renderMarkdown(report, { name: info.name, id: info.id }, frontMatter, now), 'utf8')

  const dir = resolveExportDir(context, input.destDir, info.name)
  const base = `${report.period.startDate}_${periodEndDate(report.period)}_weekly-report${ext}`
  const target = path.join(dir, base)

  if (context.fileSystem.exists(target) && input.overwrite !== true) {
    throw new AppError(AppErrorCode.Conflict, `目标文件已存在：${target}`, {
      reason: 'export.exists',
      path: target
    })
  }
  writeAtomically(context.fileSystem, dir, base, payload)
  return { path: target, format, bytes: payload.byteLength, status: report.status }
}

function normalizeFormat(raw: ExportInput['format']): 'md' | 'html' {
  const format = (raw ?? 'md').toLowerCase().trim()
  if (format !== 'md' && format !== 'html') {
    throw new AppError(AppErrorCode.InvalidArgument, '仅支持 Markdown（md）与 HTML（html）导出', {
      reason: 'export.unsupported_format',
      format: raw
    })
  }
  return format
}

/**
 * Resolves the destination directory. An explicit absolute path outside the
 * workspace is valid; a relative path, a missing parent or the project
 * directory itself is `INVALID_ARGUMENT`; an unwritable directory is
 * `EXTERNAL_UNAVAILABLE`.
 */
export function resolveExportDir(
  context: WeeklyContext,
  explicit: string | undefined,
  projectName: string
): string {
  const fs = context.fileSystem
  if (explicit !== undefined && explicit.trim() !== '') {
    const cleaned = path.resolve(explicit)
    if (!path.isAbsolute(explicit)) {
      throw new AppError(AppErrorCode.InvalidArgument, '导出目录必须是绝对路径', {
        reason: 'export.invalid_path',
        path: explicit
      })
    }
    const projectDir = path.join(context.root, 'projects', context.projectId)
    if (cleaned === projectDir || cleaned.startsWith(`${projectDir}${path.sep}`)) {
      throw new AppError(AppErrorCode.InvalidArgument, '导出目录不能位于项目目录内', {
        reason: 'export.invalid_path',
        path: cleaned
      })
    }
    if (fs.exists(cleaned)) {
      const info = statOrThrow(fs, cleaned)
      if (!info.isDirectory()) {
        throw new AppError(AppErrorCode.InvalidArgument, `导出目标不是目录：${cleaned}`, {
          reason: 'export.dir_missing',
          path: cleaned
        })
      }
    } else {
      const parent = path.dirname(cleaned)
      if (parent === cleaned || !fs.exists(parent) || !statOrThrow(fs, parent).isDirectory()) {
        throw new AppError(AppErrorCode.InvalidArgument, `导出目录的上级目录不存在：${cleaned}`, {
          reason: 'export.dir_missing',
          path: cleaned
        })
      }
      try {
        fs.mkdir(cleaned, { recursive: true })
      } catch (error) {
        throw mapFsError(error, 'export.dir_missing', cleaned)
      }
    }
    try {
      fs.access(cleaned, constants.W_OK)
    } catch (error) {
      throw mapFsError(error, 'export.unwritable', cleaned)
    }
    return cleaned
  }

  const slug = slugify(projectName)
  const dir = path.join(context.root, 'exports', slug === '' ? `project-${shortId(context.projectId)}` : slug)
  if (fs.exists(dir)) {
    if (!statOrThrow(fs, dir).isDirectory()) {
      throw new AppError(AppErrorCode.InvalidArgument, `导出路径不是目录：${dir}`, {
        reason: 'export.dir_missing',
        path: dir
      })
    }
  } else {
    try {
      fs.mkdir(dir, { recursive: true })
    } catch (error) {
      throw mapFsError(error, 'export.dir_missing', dir)
    }
  }
  try {
    fs.access(dir, constants.W_OK)
  } catch (error) {
    throw mapFsError(error, 'export.unwritable', dir)
  }
  return dir
}

function statOrThrow(fs: WeeklyFileSystem, target: string): { isDirectory(): boolean; isFile(): boolean } {
  try {
    return fs.stat(target)
  } catch (error) {
    throw mapFsError(error, 'export.dir_missing', target)
  }
}

function writeAtomically(fs: WeeklyFileSystem, dir: string, base: string, payload: Buffer): void {
  tempCounter += 1
  const temp = path.join(dir, `.${base}.${process.pid}-${tempCounter}.part`)
  try {
    fs.writeFile(temp, payload, { exclusive: true })
  } catch (error) {
    safeUnlink(fs, temp)
    throw mapFsError(error, 'export.write_failed', temp)
  }
  try {
    fs.rename(temp, path.join(dir, base))
  } catch (error) {
    safeUnlink(fs, temp)
    throw mapFsError(error, 'export.write_failed', path.join(dir, base))
  }
}

function safeUnlink(fs: WeeklyFileSystem, target: string): void {
  try {
    if (fs.exists(target)) fs.unlink(target)
  } catch {
    // Cleanup failure must not mask the original error.
  }
}

export function mapFsError(error: unknown, reason: string, target: string): AppError {
  const errno = errorCode(error)
  const label = errno === '' ? 'UNKNOWN' : errno
  const details: Record<string, unknown> = { reason, path: target, errno: label }
  if (label === 'EACCES' || label === 'EPERM' || label === 'EROFS' || label === 'ENOSPC' || label === 'EDQUOT' || label === 'EMFILE' || label === 'ENFILE') {
    return new AppError(AppErrorCode.ExternalUnavailable, `导出目录不可用（${label}）：${target}`, details)
  }
  if (label === 'EEXIST') {
    return new AppError(AppErrorCode.Conflict, `目标文件已存在：${target}`, {
      reason: 'export.exists',
      path: target
    })
  }
  if (label === 'ENOENT' || label === 'ENOTDIR' || label === 'ENAMETOOLONG' || label === 'EINVAL') {
    return new AppError(AppErrorCode.InvalidArgument, `导出路径不可用：${target}`, details)
  }
  // Anything else is still an environment problem, never a masked INTERNAL.
  return new AppError(AppErrorCode.ExternalUnavailable, `导出失败（${label}）：${target}`, details)
}

function errorCode(error: unknown): string {
  if (typeof error !== 'object' || error === null || !('code' in error)) return ''
  const code = error.code
  return typeof code === 'string' ? code : ''
}

function slugify(name: string): string {
  const slug = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return slug
}

function shortId(id: string): string {
  return id.replaceAll('-', '').slice(0, 8)
}

/* ---------------------------------------------------------------- render */

function yamlString(value: string): string {
  return JSON.stringify(value)
}

export function renderMarkdown(
  report: WeeklyReport,
  project: { name: string; id: string },
  frontMatter: boolean,
  now: Date
): string {
  let head = ''
  if (frontMatter) {
    const generation = report.meta.generation
    const generatedBy = generation?.mode === 'ai' ? 'ai' : 'manual'
    head += '---\n'
    head += `project: ${yamlString(project.name)}\n`
    head += `project_id: ${yamlString(project.id)}\n`
    head += `period_start: ${yamlString(report.period.startDate)}\n`
    head += `period_end: ${yamlString(periodEndDate(report.period))}\n`
    head += `status: ${yamlString(report.status)}\n`
    head += `generated_by: ${yamlString(generatedBy)}\n`
    if (generation?.prompt_version !== undefined && generation.prompt_version !== '') {
      head += `prompt_version: ${yamlString(generation.prompt_version)}\n`
    }
    head += `exported_at: ${yamlString(now.toISOString())}\n`
    head += '---\n\n'
  }
  return head + (report.contentMd.endsWith('\n') ? report.contentMd : `${report.contentMd}\n`)
}

export function renderHtml(
  report: WeeklyReport,
  project: { name: string },
  frontMatter: boolean,
  now: Date
): Buffer {
  const period = `${report.period.startDate} – ${periodEndDate(report.period)}`
  const title = `${project.name} 周报`
  const parts: string[] = [
    '<!DOCTYPE html>',
    '<html lang="zh-CN">',
    '<head>',
    '<meta charset="utf-8">',
    '<meta name="viewport" content="width=device-width, initial-scale=1">',
    `<title>${escapeHtml(title)} · ${escapeHtml(period)}</title>`,
    '<style>',
    REPORT_CSS,
    '</style>',
    '</head>',
    '<body>',
    '<main class="report">',
    '<header class="report-head">',
    '<p class="eyebrow">Weekly Report</p>',
    `<h1>${escapeHtml(project.name)}</h1>`,
    `<p class="period">${escapeHtml(period)}<span class="dot">·</span>${daysOf(report)} 天</p>`
  ]
  if (frontMatter) {
    const status = report.status === 'confirmed' ? '已确认' : '草稿'
    parts.push(`<p class="meta"><span class="pill ${report.status}">${status}</span>`)
    if (report.confirmedAt !== undefined) {
      parts.push(`<span>确认于 ${escapeHtml(formatConfirmed(report.confirmedAt))}</span>`)
    }
    if (report.meta.generation?.mode === 'ai') parts.push('<span>AI 初稿</span>')
    parts.push('</p>')
  }
  parts.push('</header>', '<article class="report-body">')
  parts.push(renderMarkdownHtml(report.contentMd))
  parts.push('</article>', '<footer class="report-foot">')
  parts.push(`<span>ResearchOS · ${escapeHtml(now.toISOString().slice(0, 16).replace('T', ' '))} 导出</span>`)
  parts.push(`<span>${escapeHtml(project.name)}</span>`)
  parts.push('</footer>', '</main>', '</body>', '</html>')
  return Buffer.from(`${parts.join('\n')}\n`, 'utf8')
}

function daysOf(report: WeeklyReport): number {
  const start = Date.parse(`${report.period.startDate}T00:00:00Z`)
  const end = Date.parse(`${report.period.endExclusiveDate}T00:00:00Z`)
  return Math.round((end - start) / 86_400_000)
}

function formatConfirmed(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 10)} ${date.toISOString().slice(11, 16)}`
}

export function escapeHtml(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;')
}

const REF_TOKEN = /\[\[ref:([a-z_]+):([0-9a-fA-F-]{36})\]\]/g
const LINK_TOKEN = /\[([^\]]+)\]\(([^)\s]+)\)/g
const BOLD = /\*\*([^*]+)\*\*/g
const ITALIC = /\*([^*\n]+)\*/g
const CODE = /`([^`]+)`/g
const ORDERED = /^([0-9]{1,3})[.)]\s+(.*)$/
const BULLET = /^[-*+]\s+(.*)$/

/**
 * Renders the Markdown subset the report template emits: ATX headings,
 * bullets (one nesting level), ordered lists, quotes, fenced code, rules and
 * paragraphs. The single H1 is dropped because the header repeats it.
 */
export function renderMarkdownHtml(markdown: string): string {
  const out: string[] = []
  const lines = markdown.replaceAll('\r\n', '\n').split('\n')
  let listTag = ''
  let inCode = false
  let paragraph: string[] = []
  let quote: string[] = []

  const closeList = (): void => {
    if (listTag !== '') {
      out.push(`</${listTag}>`)
      listTag = ''
    }
  }
  const flushQuote = (): void => {
    if (quote.length === 0) return
    out.push(`<blockquote><p>${inlineHtml(quote.join(' '))}</p></blockquote>`)
    quote = []
  }
  const flushParagraph = (): void => {
    if (paragraph.length === 0) return
    out.push(`<p>${inlineHtml(paragraph.join(' '))}</p>`)
    paragraph = []
  }

  for (const raw of lines) {
    const line = raw.replace(/[ \t]+$/, '')
    const trimmed = line.trim()
    if (inCode) {
      if (trimmed.startsWith('```')) {
        out.push('</code></pre>')
        inCode = false
      } else {
        out.push(escapeHtml(raw))
      }
      continue
    }
    if (trimmed.startsWith('```')) {
      flushParagraph()
      closeList()
      flushQuote()
      out.push('<pre><code>')
      inCode = true
      continue
    }
    if (trimmed === '') {
      flushParagraph()
      closeList()
      flushQuote()
      continue
    }
    const heading = headingLine(trimmed)
    if (heading !== undefined) {
      flushParagraph()
      closeList()
      flushQuote()
      if (heading.level === 1) continue
      out.push(`<h${heading.level}>${inlineHtml(heading.text)}</h${heading.level}>`)
      continue
    }
    if (trimmed === '---' || trimmed === '***') {
      flushParagraph()
      closeList()
      flushQuote()
      out.push('<hr>')
      continue
    }
    if (trimmed.startsWith('>')) {
      flushParagraph()
      closeList()
      quote.push(trimmed.slice(1).trim())
      continue
    }
    const bullet = BULLET.exec(trimmed)
    if (bullet !== null) {
      flushParagraph()
      flushQuote()
      if (listTag !== 'ul') {
        closeList()
        listTag = 'ul'
        out.push('<ul>')
      }
      const nested = line.length - line.trimStart().length >= 2 ? ' class="nested"' : ''
      out.push(`<li${nested}>${inlineHtml(bullet[1] ?? '')}</li>`)
      continue
    }
    const ordered = ORDERED.exec(trimmed)
    if (ordered !== null) {
      flushParagraph()
      flushQuote()
      if (listTag !== 'ol') {
        closeList()
        listTag = 'ol'
        out.push('<ol>')
      }
      out.push(`<li>${inlineHtml(ordered[2] ?? '')}</li>`)
      continue
    }
    flushQuote()
    paragraph.push(trimmed)
  }
  if (inCode) out.push('</code></pre>')
  flushParagraph()
  closeList()
  flushQuote()
  return out.join('\n')
}

function headingLine(line: string): { level: number; text: string } | undefined {
  let level = 0
  while (level < line.length && line[level] === '#') level += 1
  if (level === 0 || level > 6 || level >= line.length || line[level] !== ' ') return undefined
  return { level, text: line.slice(level + 1).trim() }
}

function inlineHtml(text: string): string {
  let out = escapeHtml(text)
  out = out.replace(REF_TOKEN, (_match, type: string, id: string) => {
    const short = id.replaceAll('-', '').slice(0, 8)
    return `<span class="ref" title="${type}:${id}">${type}:${short}</span>`
  })
  out = out.replace(LINK_TOKEN, (_match, label: string, url: string) => {
    if (!safeLink(url)) return label
    return `<a href="${url}">${label}</a>`
  })
  out = out.replace(BOLD, '<strong>$1</strong>')
  out = out.replace(ITALIC, '<em>$1</em>')
  out = out.replace(CODE, '<code>$1</code>')
  return out
}

function safeLink(url: string): boolean {
  const lower = url.toLowerCase()
  return (
    lower.startsWith('http://') ||
    lower.startsWith('https://') ||
    lower.startsWith('mailto:') ||
    lower.startsWith('#')
  )
}

const REPORT_CSS = `:root{color-scheme:light dark;--fg:#1b1f24;--muted:#5b6470;--border:#e3e7ec;--surface:#fff;--accent:#3b6ef0;--accent-soft:#eef3ff;--page:#f6f7f9}
@media (prefers-color-scheme:dark){:root{--fg:#e6e9ee;--muted:#9aa4b2;--border:#2b3138;--surface:#171a1e;--accent:#7aa2ff;--accent-soft:#1d2430;--page:#101215}}
*{box-sizing:border-box}
body{margin:0;padding:48px 20px 64px;background:var(--page);color:var(--fg);font:15px/1.7 "Inter","Segoe UI","PingFang SC","Microsoft YaHei",system-ui,sans-serif;-webkit-font-smoothing:antialiased}
.report{max-width:820px;margin:0 auto}
.report-head{padding:0 4px 24px;border-bottom:1px solid var(--border)}
.eyebrow{margin:0 0 6px;font-size:12px;letter-spacing:.14em;text-transform:uppercase;color:var(--accent)}
h1{margin:0 0 8px;font-size:30px;line-height:1.25;letter-spacing:-.01em}
.period{margin:0;color:var(--muted);font-variant-numeric:tabular-nums}
.dot{margin:0 8px;opacity:.55}
.meta{display:flex;flex-wrap:wrap;gap:8px 16px;align-items:center;margin:14px 0 0;color:var(--muted);font-size:13px}
.pill{padding:2px 10px;border-radius:999px;border:1px solid var(--border);font-size:12px}
.pill.confirmed{color:#1a7f4b;border-color:#b7e2c9;background:#ecf8f1}
.pill.draft{color:#9a5b00;border-color:#f0d9ae;background:#fdf4e3}
@media (prefers-color-scheme:dark){.pill.confirmed{color:#6fd39b;border-color:#1f4a35;background:#11251b}.pill.draft{color:#e0ab5c;border-color:#4a3a1c;background:#241d10}}
.report-body{display:block;margin-top:8px}
.report-body h2{margin:34px 0 12px;padding:14px 18px;font-size:17px;background:var(--surface);border:1px solid var(--border);border-left:3px solid var(--accent);border-radius:10px}
.report-body h3{margin:22px 0 8px;font-size:15px}
.report-body p,.report-body ul,.report-body ol,.report-body blockquote,.report-body pre{margin:0 0 10px}
.report-body ul,.report-body ol{padding-left:22px}
.report-body li{margin:4px 0}
.report-body li.nested{margin-left:18px;color:var(--muted)}
.report-body strong{color:var(--fg)}
.report-body a{color:var(--accent);text-decoration:none;border-bottom:1px solid color-mix(in srgb,var(--accent) 40%,transparent)}
.report-body code{padding:1px 5px;border-radius:5px;background:var(--accent-soft);font-family:"SFMono-Regular",ui-monospace,"Consolas",monospace;font-size:13px}
.report-body pre{padding:14px 16px;overflow:auto;background:var(--surface);border:1px solid var(--border);border-radius:10px}
.report-body pre code{background:none;padding:0}
.report-body blockquote{padding:10px 16px;color:var(--muted);border-left:3px solid var(--border);background:var(--surface)}
.report-body hr{margin:26px 0;border:0;border-top:1px solid var(--border)}
.ref{padding:0 5px;border-radius:4px;font-size:11px;letter-spacing:.02em;color:var(--muted);background:var(--accent-soft);white-space:nowrap}
.report-foot{display:flex;justify-content:space-between;gap:12px;margin-top:44px;padding-top:16px;border-top:1px solid var(--border);color:var(--muted);font-size:12px}
@media print{body{padding:0;background:#fff;color:#000}.report{max-width:none}.report-body h2{break-after:avoid;page-break-inside:avoid}.report-body li{break-inside:avoid}.report-foot{display:none}}`