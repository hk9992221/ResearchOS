import { Navigate, Route, Routes } from 'react-router-dom'

import { ROUTE_REGISTRY } from '../routes'
import { RouteOutlet } from './RouteOutlet'

/**
 * AppRoutes maps the registry onto the router. Unknown hash paths return to
 * Home: a file:// launch may carry an empty hash, and a stale link should not
 * dead-end the window.
 */
export function AppRoutes(): JSX.Element {
  return (
    <Routes>
      {ROUTE_REGISTRY.map((entry) => (
        <Route key={entry.kind} path={entry.path} element={<RouteOutlet entry={entry} />} />
      ))}
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}