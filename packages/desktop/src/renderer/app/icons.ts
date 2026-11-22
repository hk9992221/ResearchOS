/**
 * Icon registry.
 *
 * Every icon is a Lucide outline, as in internal/ui/icons: the Go registry
 * embeds 42 Lucide SVGs, and the Electron shell takes the same glyphs from
 * `lucide-react` under the same semantics. Modules take icons from here, never
 * from an ad-hoc import, so the stroke, size and meaning stay consistent.
 */

import {
  Archive,
  ArchiveRestore,
  ArrowLeft,
  BookOpen,
  CalendarDays,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  CircleCheck,
  CircleHelp,
  CircleX,
  Clock,
  Command,
  Copy,
  ExternalLink,
  FileText,
  FlaskConical,
  Folder,
  FolderOpen,
  House,
  Inbox,
  Info,
  Languages,
  LayoutDashboard,
  Lightbulb,
  Link,
  LoaderCircle,
  Monitor,
  Moon,
  MoreHorizontal,
  NotebookPen,
  PanelLeft,
  Plus,
  RefreshCw,
  ScanLine,
  Search,
  Settings,
  SquareKanban,
  SquarePen,
  Stethoscope,
  Store,
  Sun,
  Trash2,
  TriangleAlert,
  X,
  type LucideIcon
} from 'lucide-react'

export const ICONS = {
  home: House,
  projects: Folder,
  folderOpen: FolderOpen,
  inbox: Inbox,
  pluginMarket: Store,
  settings: Settings,
  help: CircleHelp,
  overview: LayoutDashboard,
  literature: BookOpen,
  note: NotebookPen,
  ideas: Lightbulb,
  experiments: FlaskConical,
  kanban: SquareKanban,
  weeklyReview: CalendarDays,
  search: Search,
  command: Command,
  trash: Trash2,
  refresh: RefreshCw,
  scan: ScanLine,
  diagnostics: Stethoscope,
  chevronDown: ChevronDown,
  chevronRight: ChevronRight,
  chevronLeft: ChevronLeft,
  chevronUp: ChevronUp,
  close: X,
  check: Check,
  warning: TriangleAlert,
  info: Info,
  plus: Plus,
  externalLink: ExternalLink,
  link: Link,
  copy: Copy,
  loader: LoaderCircle,
  sidebar: PanelLeft,
  fileText: FileText,
  more: MoreHorizontal,
  back: ArrowLeft,
  success: CircleCheck,
  failure: CircleX,
  clock: Clock,
  edit: SquarePen,
  archive: Archive,
  archiveRestore: ArchiveRestore,
  // Shell controls the Go UI renders with text menus or system glyphs.
  sun: Sun,
  moon: Moon,
  system: Monitor,
  language: Languages,
  panelLeft: Monitor
} as const satisfies Record<string, LucideIcon>

export type IconName = keyof typeof ICONS

export function iconFor(name: IconName): LucideIcon {
  return ICONS[name]
}