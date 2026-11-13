package dashboard

import (
	"sort"
	"time"

	"researchos/internal/project"
)

// This file owns the snapshot cache. The cache stores exactly one whole
// Snapshot: §5.11 asks for a 60s whole-page cache and lets the next Load
// rebuild everything, so a per-card cache would add state without changing what
// the UI sees. Invalidate and InvalidateAll therefore drop the same entry.

// cloneSnapshot copies the read model under the cache lock. The returned value
// owns its slices and timestamps, so a caller cannot mutate the cache. A nil
// snapshot stays nil.
func cloneSnapshot(in *Snapshot) *Snapshot {
	if in == nil {
		return nil
	}
	out := *in
	out.Projects = make([]ProjectCard, len(in.Projects))
	for i, card := range in.Projects {
		out.Projects[i] = cloneCard(card)
	}
	out.RecentActivity = append([]project.ActivityItem(nil), in.RecentActivity...)
	out.Degraded = append([]string(nil), in.Degraded...)
	return &out
}

// cloneCard copies one card including its LastActivityAt pointer, which is the
// only reference a card shares with another value.
func cloneCard(card ProjectCard) ProjectCard {
	if card.LastActivityAt == nil {
		return card
	}
	at := *card.LastActivityAt
	card.LastActivityAt = &at
	card.Stats.LastActivityAt = &at
	return card
}

// cachedSnapshot returns the current generation together with a copy of the
// cached snapshot, or a nil snapshot when the cache is empty or expired. The
// copy is made while holding mu: RefreshCard rewrites cards in place, so reading
// the cached slice outside the lock would race with it. The generation lets Load
// detect an Invalidate that landed while it was building.
func (d *Service) cachedSnapshot() (uint64, *Snapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached == nil || !d.clock.Now().Before(d.expires) {
		return d.gen, nil
	}
	return d.gen, cloneSnapshot(d.cached)
}

// storeSnapshot publishes a freshly built snapshot unless the cache was
// invalidated in the meantime: resurrecting data that Invalidate already
// dropped would make the next Load serve stale projects.
func (d *Service) storeSnapshot(gen uint64, snap *Snapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen {
		return
	}
	d.cached = snap
	d.expires = d.clock.Now().Add(d.cacheTTL)
}

// updateCachedCard replaces one card inside the live snapshot and recomputes the
// totals and the degraded list. An empty or expired cache is left untouched:
// RefreshCard's own result is the answer and the next Load rebuilds the page.
func (d *Service) updateCachedCard(card ProjectCard, healthy bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached == nil || !d.clock.Now().Before(d.expires) {
		return
	}
	snap := d.cached
	idx := -1
	for i := range snap.Projects {
		if snap.Projects[i].Project.ID == card.Project.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		// A project registered after the snapshot was built appears with the
		// next Load; appending here would break the active/archived ordering.
		return
	}
	snap.Projects[idx] = cloneCard(card)
	snap.Totals = totalsOf(snap.Projects)
	snap.Degraded = setDegraded(snap.Degraded, card.Project.ID, healthy)
}

// setDegraded adds or removes one project id and keeps the list sorted, so the
// field is stable regardless of whether Load or RefreshCard produced it.
func setDegraded(list []string, projectID string, degraded bool) []string {
	pos := sort.SearchStrings(list, projectID)
	found := pos < len(list) && list[pos] == projectID
	switch {
	case degraded && !found:
		list = append(list, "")
		copy(list[pos+1:], list[pos:])
		list[pos] = projectID
	case !degraded && found:
		list = append(list[:pos], list[pos+1:]...)
	}
	return list
}

// Invalidate drops the cached snapshot after projectID changed. §5.11 only
// promises that the next Load sees the change, and dropping the whole read model
// is the simplest correct way to honor it; a targeted card refresh is
// RefreshCard's job. Idempotent and safe to call concurrently.
func (d *Service) Invalidate(projectID string) {
	d.dropSnapshot()
}

// InvalidateAll drops the cached snapshot unconditionally. Idempotent and safe
// to call concurrently.
func (d *Service) InvalidateAll() {
	d.dropSnapshot()
}

// dropSnapshot clears the cache and advances the generation so an in-flight
// build cannot publish its pre-invalidation result.
func (d *Service) dropSnapshot() {
	d.mu.Lock()
	d.cached = nil
	d.expires = time.Time{}
	d.gen++
	d.mu.Unlock()
}
