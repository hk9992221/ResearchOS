// Package confirm implements the in-app human confirmation tokens required by
// destructive operations (SPEC-01 §5.8). Tokens live in memory only: they are
// never persisted and never cross the process boundary, so an MCP client cannot
// mint one.
package confirm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/errs"
)

// Kind is the operation a token authorizes.
type Kind string

const (
	// KindPurgeEntity authorizes permanently deleting specific entities.
	KindPurgeEntity Kind = "purge_entity"
	// KindEmptyTrash authorizes purging the whole project trash.
	KindEmptyTrash Kind = "empty_trash"
	// KindDeleteProject authorizes deleting a project and its data.
	KindDeleteProject Kind = "delete_project"
	// KindPDFOverwrite authorizes overwriting a local PDF copy (SPEC-04).
	KindPDFOverwrite Kind = "pdf_overwrite"
)

// Valid reports whether k is a registered kind.
func (k Kind) Valid() bool {
	switch k {
	case KindPurgeEntity, KindEmptyTrash, KindDeleteProject, KindPDFOverwrite:
		return true
	default:
		return false
	}
}

// Scope binds a token to one operation and one target set. EntityTypes and
// EntityIDs are compared as sets, so order and duplicates do not matter.
type Scope struct {
	Kind        Kind
	ProjectID   string
	EntityTypes []string
	EntityIDs   []string
}

// Token is an opaque, one-time, process-local confirmation token.
type Token string

// Service issues and consumes confirmation tokens.
type Service interface {
	// Issue is only called from the in-app human confirmation callback.
	Issue(ctx context.Context, sc Scope) (Token, error)
	// Consume invalidates the token. Unknown, expired and already-used tokens
	// yield ErrUserConfirmationRequired; a scope mismatch yields
	// ErrConfirmScopeMismatch and leaves the token usable.
	Consume(ctx context.Context, t Token, sc Scope) error
}

// DefaultTTL is the token lifetime from §5.8.
const DefaultTTL = 5 * time.Minute

// Options configures a Service.
type Options struct {
	// Clock supplies expiry timestamps. Nil means clock.System.
	Clock clock.Clock
	// TTL overrides the token lifetime. Zero means DefaultTTL.
	TTL time.Duration
}

// New returns the in-memory confirmation service.
func New(opt Options) Service {
	c := opt.Clock
	if c == nil {
		c = clock.System{}
	}
	ttl := opt.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &service{clock: c, ttl: ttl, tokens: make(map[Token]entry)}
}

type entry struct {
	scope   Scope
	expires time.Time
}

type service struct {
	mu     sync.Mutex
	clock  clock.Clock
	ttl    time.Duration
	tokens map[Token]entry
}

// Issue implements Service.
func (s *service) Issue(ctx context.Context, sc Scope) (Token, error) {
	if !sc.Kind.Valid() {
		return "", fmt.Errorf("confirm: unknown kind %q", sc.Kind)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("confirm: generate token: %w", err)
	}
	token := Token(hex.EncodeToString(raw))
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.tokens[token] = entry{scope: normalize(sc), expires: now.Add(s.ttl)}
	return token, nil
}

// Consume implements Service.
func (s *service) Consume(ctx context.Context, t Token, sc Scope) error {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)

	e, ok := s.tokens[t]
	if !ok {
		return fmt.Errorf("%w: token is unknown, expired, or already used", errs.ErrUserConfirmationRequired)
	}
	if !now.Before(e.expires) {
		delete(s.tokens, t)
		return fmt.Errorf("%w: token expired", errs.ErrUserConfirmationRequired)
	}
	if !scopeMatches(e.scope, normalize(sc)) {
		return fmt.Errorf("%w: confirmation scope does not match the requested operation", errs.ErrConfirmScopeMismatch)
	}
	delete(s.tokens, t)
	return nil
}

// pruneLocked drops expired entries. Callers hold s.mu.
func (s *service) pruneLocked(now time.Time) {
	for token, e := range s.tokens {
		if !now.Before(e.expires) {
			delete(s.tokens, token)
		}
	}
}

// normalize returns a copy with de-duplicated, sorted type and id sets.
func normalize(sc Scope) Scope {
	return Scope{
		Kind:        sc.Kind,
		ProjectID:   sc.ProjectID,
		EntityTypes: sortedSet(sc.EntityTypes),
		EntityIDs:   sortedSet(sc.EntityIDs),
	}
}

func sortedSet(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	for _, v := range in {
		seen[v] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func scopeMatches(a, b Scope) bool {
	return a.Kind == b.Kind && a.ProjectID == b.ProjectID &&
		sameSet(a.EntityTypes, b.EntityTypes) && sameSet(a.EntityIDs, b.EntityIDs)
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
