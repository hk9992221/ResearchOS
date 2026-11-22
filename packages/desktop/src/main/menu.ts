import { app, Menu, type MenuItemConstructorOptions } from 'electron'

/**
 * Application menu only; every actionable control lives in the renderer. The
 * menu exposes standard roles plus dev-only reload/devtools entries.
 */
export function buildApplicationMenuTemplate(isDev: boolean): MenuItemConstructorOptions[] {
  const isMac = process.platform === 'darwin'
  const template: MenuItemConstructorOptions[] = []

  if (isMac) {
    template.push({
      label: app.name,
      submenu: [
        { role: 'about' },
        { type: 'separator' },
        { role: 'services' },
        { type: 'separator' },
        { role: 'hide' },
        { role: 'hideOthers' },
        { role: 'unhide' },
        { type: 'separator' },
        { role: 'quit' }
      ]
    })
  }

  template.push({
    label: 'File',
    submenu: [isMac ? { role: 'close' } : { role: 'quit' }]
  })

  template.push({
    label: 'Edit',
    submenu: [
      { role: 'undo' },
      { role: 'redo' },
      { type: 'separator' },
      { role: 'cut' },
      { role: 'copy' },
      { role: 'paste' },
      { role: 'selectAll' }
    ]
  })

  const viewSubmenu: MenuItemConstructorOptions[] = [
    { role: 'resetZoom' },
    { role: 'zoomIn' },
    { role: 'zoomOut' },
    { type: 'separator' },
    { role: 'togglefullscreen' }
  ]
  if (isDev) {
    viewSubmenu.push(
      { type: 'separator' },
      { role: 'reload' },
      { role: 'forceReload' },
      { role: 'toggleDevTools' }
    )
  }
  template.push({ label: 'View', submenu: viewSubmenu })

  template.push({
    label: 'Window',
    submenu: [
      { role: 'minimize' },
      { role: 'zoom' },
      ...(isMac ? [{ type: 'separator' as const }, { role: 'front' as const }] : [])
    ]
  })

  template.push({
    label: 'Help',
    submenu: [{ role: 'about', label: 'About ResearchOS' }]
  })

  return template
}

export function installApplicationMenu(isDev: boolean): Menu {
  const menu = Menu.buildFromTemplate(buildApplicationMenuTemplate(isDev))
  Menu.setApplicationMenu(menu)
  return menu
}