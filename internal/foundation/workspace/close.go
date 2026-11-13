package workspace

// CloseProject closes and evicts the cached handle of one project. Without the
// eviction the next Project call would hand back a handle whose pool is
// already closed. Closing before a directory removal also matters on Windows,
// where an open project.db keeps the files locked.
//
// It is a no-op when the project was never opened.
func (w *Workspace) CloseProject(projectID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	h, ok := w.projects[projectID]
	if !ok {
		return nil
	}
	delete(w.projects, projectID)
	return h.Close()
}
