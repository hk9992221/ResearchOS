import { useEffect, useState } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { HashRouter, Route, Routes } from 'react-router-dom'

import { AppShell, type AppShellProps } from './app/AppShell'
import { ThemeProvider, type ThemeMode } from './design/theme'
import WorkspaceWizard from './features/wizard/WorkspaceWizard'
import { I18nProvider, type LanguagePreference } from './i18n'
import { createQueryClient, wireAppEvents } from './lib/query'

export interface AppProps extends AppShellProps {
  /** Test/host seam; the stored preference is used otherwise. */
  readonly initialTheme?: ThemeMode
  readonly initialLanguage?: LanguagePreference
}

/**
 * App is the composition root of the renderer: language, appearance, the
 * query cache, app-event wiring, the hash router and the application shell.
 *
 * HashRouter is required because the packaged app loads from file://, where
 * the history API has no origin to resolve paths against. `/welcome` sits
 * outside the shell: it is the setup destination the workspace gate routes to
 * before any project can exist.
 */
export default function App({
  initialTheme,
  initialLanguage,
  ...shell
}: AppProps): JSX.Element {
  const [queryClient] = useState(createQueryClient)

  useEffect(() => wireAppEvents(queryClient), [queryClient])

  return (
    <I18nProvider initialPreference={initialLanguage}>
      <ThemeProvider initialMode={initialTheme}>
        <QueryClientProvider client={queryClient}>
          <HashRouter>
            <Routes>
              <Route path="/welcome" element={<WorkspaceWizard />} />
              <Route path="*" element={<AppShell {...shell} />} />
            </Routes>
          </HashRouter>
        </QueryClientProvider>
      </ThemeProvider>
    </I18nProvider>
  )
}