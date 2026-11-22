import { existsSync } from 'node:fs'
import { join } from 'node:path'

import { app, BrowserWindow, screen, shell } from 'electron'

import {
  clampWindowState,
  defaultWindowState,
  MIN_WINDOW_HEIGHT,
  MIN_WINDOW_WIDTH,
  readWindowState,
  writeWindowState,
  type DisplayBounds
} from './window-state'

const SAVE_DEBOUNCE_MS = 300

function currentDisplays(): DisplayBounds[] {
  return screen.getAllDisplays().map((display) => ({
    x: display.workArea.x,
    y: display.workArea.y,
    width: display.workArea.width,
    height: display.workArea.height
  }))
}

/** Packaged builds ship the icon via extraResources; dev reads the repo copy. */
export function resolveAppIcon(): string | undefined {
  const packaged = join(process.resourcesPath, 'appicon.png')
  if (existsSync(packaged)) return packaged

  const development = join(app.getAppPath(), '..', '..', 'resources', 'appicon.png')
  return existsSync(development) ? development : undefined
}

export function createMainWindow(): BrowserWindow {
  const userDataDirectory = app.getPath('userData')
  const displays = currentDisplays()
  const saved = readWindowState(userDataDirectory)
  const geometry = saved === null ? defaultWindowState(displays[0]) : clampWindowState(saved, displays)

  const window = new BrowserWindow({
    width: geometry.width,
    height: geometry.height,
    x: geometry.x,
    y: geometry.y,
    minWidth: MIN_WINDOW_WIDTH,
    minHeight: MIN_WINDOW_HEIGHT,
    title: 'ResearchOS',
    show: false,
    backgroundColor: '#0f1115',
    icon: resolveAppIcon(),
    webPreferences: {
      preload: join(__dirname, '../preload/index.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true
    }
  })

  if (geometry.maximized) window.maximize()

  let saveTimer: NodeJS.Timeout | undefined

  const persistState = (): void => {
    if (window.isDestroyed()) return
    const bounds = window.isMaximized() ? window.getNormalBounds() : window.getBounds()
    try {
      writeWindowState(userDataDirectory, { ...bounds, maximized: window.isMaximized() })
    } catch (error) {
      console.warn('window state save failed', error)
    }
  }

  const scheduleStateSave = (): void => {
    clearTimeout(saveTimer)
    saveTimer = setTimeout(persistState, SAVE_DEBOUNCE_MS)
  }

  window.on('resize', scheduleStateSave)
  window.on('move', scheduleStateSave)
  window.on('maximize', scheduleStateSave)
  window.on('unmaximize', scheduleStateSave)
  window.on('close', persistState)
  window.on('closed', () => {
    clearTimeout(saveTimer)
    saveTimer = undefined
  })

  window.once('ready-to-show', () => window.show())

  window.webContents.setWindowOpenHandler(({ url }) => {
    void shell.openExternal(url)
    return { action: 'deny' }
  })
  window.webContents.on('will-navigate', (event) => event.preventDefault())

  const rendererUrl = process.env['ELECTRON_RENDERER_URL']
  if (rendererUrl !== undefined && rendererUrl !== '') {
    void window.loadURL(rendererUrl)
  } else {
    void window.loadFile(join(__dirname, '../renderer/index.html'))
  }

  return window
}