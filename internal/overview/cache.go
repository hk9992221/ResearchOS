package overview

import "time"

// cacheEntry is the cached Overview read of one project. It keeps the snapshot
// after its TTL expires so a later timeout can still fall back to the last known
// data instead of showing an empty card (§6.3 "保留上次数据并显示刷新按钮").
//
// Snapshots are treated as immutable once stored: Refresh copies the struct and
// replaces one widget payload instead of mutating a snapshot another goroutine
// may be reading.
type cacheEntry struct {
	snap   *Snapshot
	stored time.Time
}

// fresh reports whether the entry still answers Load without requerying. §5.6
// sets the window at 15s (Options.CacheTTL).
func (e *cacheEntry) fresh(now time.Time, ttl time.Duration) bool {
	return e != nil && now.Sub(e.stored) < ttl
}

// entry returns the raw cache slot of one project, including an expired entry.
// A closed workspace or an unknown project yields nil.
func (s *Service) entry(projectID string) *cacheEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cache[projectID]
}

// previous returns the last known snapshot of one project, expired or not: the
// fallback payload for a widget that fails to load.
func (s *Service) previous(projectID string) *Snapshot {
	e := s.entry(projectID)
	if e == nil {
		return nil
	}
	return e.snap
}

// put stores a snapshot and restarts its TTL.
func (s *Service) put(projectID string, snap *Snapshot, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = make(map[string]*cacheEntry)
	}
	s.cache[projectID] = &cacheEntry{snap: snap, stored: now}
}
