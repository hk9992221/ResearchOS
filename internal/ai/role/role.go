// Package role owns purpose assignments role → (provider, model), their
// validation against the protocol capability matrix, and resolution used by
// the gateway before any network call (SPEC-02 §5.6).
package role

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"researchos/internal/ai/model"
	"researchos/internal/ai/provider"
	"researchos/internal/foundation/clock"
)

// Role is an assignable purpose id (SPEC-00 §7 keeps the set open; SPEC-02
// uses exactly these three until SPEC-00 is revised).
type Role string

// The roles this SPEC uses.
const (
	RoleChatMain  Role = "chat_main"
	RoleEmbedding Role = "embedding"
	RoleRerank    Role = "rerank"
)

// Problem strings from §5.6 RoleView.
const (
	ProblemRoleUnassigned    = "role_unassigned"
	ProblemProviderDisabled  = "provider_disabled"
	ProblemModelUnavailable  = "model_unavailable"
	ProblemCapabilityMissing = "capability_missing"
)

// usage maps each role to the capability its assignments must satisfy.
var usage = map[Role]provider.Capability{
	RoleChatMain:  provider.CapabilityChat,
	RoleEmbedding: provider.CapabilityEmbedding,
	RoleRerank:    provider.CapabilityRerank,
}

// AllRoles lists the assignable roles in UI order.
func AllRoles() []Role { return []Role{RoleChatMain, RoleEmbedding, RoleRerank} }

// RequiredCapability resolves a raw role id, for injection into
// provider.Options.RoleCaps without creating a provider→role import.
func RequiredCapability(raw string) (provider.Capability, bool) {
	c, ok := usage[Role(raw)]
	return c, ok
}

// ErrUnknownRole rejects unregistered role ids.
var ErrUnknownRole = errors.New("role: unknown role")

// Error is the typed resolution failure carrying the §5.6 problem string.
type Error struct {
	Role    Role
	Problem string
	Detail  string
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("role %s: %s: %s", e.Role, e.Problem, e.Detail)
	}
	return fmt.Sprintf("role %s: %s", e.Role, e.Problem)
}

// RoleAssignment is one persisted assignment.
type RoleAssignment struct {
	Role       Role
	ProviderID string
	ModelID    string
	UpdatedAt  time.Time
}

// RoleView is the outward shape for the assignment screen (§6.3).
type RoleView struct {
	Role               Role
	RequiredCapability provider.Capability
	Assignment         *RoleAssignment
	ProviderName       string
	Resolved           bool
	Problem            string
}

// AssignResult reports the new assignment and whether the semantic index
// must be rebuilt (first embedding assignment or (provider, model) change).
type AssignResult struct {
	Assignment      RoleAssignment
	ReindexRequired bool
}

// ResolvedRole is the gateway's view of an assignment: everything needed to
// build an adapter. APIKeyRef stays inside ProviderConfig and never leaves
// the service layer (§5.6).
type ResolvedRole struct {
	Role       Role
	Capability provider.Capability
	Provider   provider.ProviderConfig
	Model      model.ModelInfo
}

// Service is the RoleService business entry.
type Service interface {
	List(ctx context.Context) ([]RoleView, error)
	Assign(ctx context.Context, role Role, providerID, modelID string) (AssignResult, error)
	Clear(ctx context.Context, role Role) error
	Resolve(ctx context.Context, role Role) (ResolvedRole, error)
}

type store struct {
	db    *sql.DB
	clock clock.Clock
}

var _ Service = (*store)(nil)

// New returns the role service over global.db.
func New(db *sql.DB, c clock.Clock) (Service, error) {
	if db == nil {
		return nil, errors.New("role: nil database")
	}
	if c == nil {
		c = clock.System{}
	}
	return &store{db: db, clock: c}, nil
}

// List reports every assignable role with its resolution problem (§5.6).
func (s *store) List(ctx context.Context) ([]RoleView, error) {
	out := make([]RoleView, 0, len(AllRoles()))
	for _, r := range AllRoles() {
		v := RoleView{Role: r, RequiredCapability: usage[r], Problem: ProblemRoleUnassigned}
		a, found, err := s.assignment(ctx, r)
		if err != nil {
			return nil, err
		}
		if found {
			v.Assignment = &a
			rr, err := s.resolve(ctx, r, a)
			if err != nil {
				var re *Error
				if errors.As(err, &re) {
					v.Problem = re.Problem
				} else {
					return nil, err
				}
			} else {
				v.Resolved = true
				v.ProviderName = rr.Provider.Name
				v.Problem = ""
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// Assign validates (§5.6 order 1–5) and upserts one assignment.
func (s *store) Assign(ctx context.Context, role Role, providerID, modelID string) (AssignResult, error) {
	required, ok := usage[role]
	if !ok {
		return AssignResult{}, fmt.Errorf("%w: %q", ErrUnknownRole, role)
	}
	p, err := s.loadProvider(ctx, providerID)
	if err != nil {
		return AssignResult{}, err
	}
	if !p.Enabled {
		return AssignResult{}, &Error{Role: role, Problem: ProblemProviderDisabled, Detail: p.Name}
	}
	if !provider.Has(p.Protocol, required) {
		return AssignResult{}, &Error{Role: role, Problem: ProblemCapabilityMissing,
			Detail: fmt.Sprintf("protocol %s lacks %s", p.Protocol, required)}
	}
	if _, err := s.loadModel(ctx, providerID, modelID, required); err != nil {
		return AssignResult{}, err
	}
	prev, hadPrev, err := s.assignment(ctx, role)
	if err != nil {
		return AssignResult{}, err
	}

	nowStr := clock.Format(s.clock.Now())
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO role_assignments (role, provider_id, model_id, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(role) DO UPDATE SET provider_id = excluded.provider_id,
			model_id = excluded.model_id, updated_at = excluded.updated_at`,
		string(role), providerID, modelID, nowStr)
	if err != nil {
		return AssignResult{}, fmt.Errorf("role: assign: %w", err)
	}

	res := AssignResult{Assignment: RoleAssignment{
		Role: role, ProviderID: providerID, ModelID: modelID, UpdatedAt: s.clock.Now(),
	}}
	if role == RoleEmbedding {
		res.ReindexRequired = !hadPrev || prev.ProviderID != providerID || prev.ModelID != modelID
	}
	return res, nil
}

// Clear removes an assignment; clearing an unset role is a no-op.
func (s *store) Clear(ctx context.Context, role Role) error {
	if _, ok := usage[role]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRole, role)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM role_assignments WHERE role = ?`, string(role))
	if err != nil {
		return fmt.Errorf("role: clear: %w", err)
	}
	return nil
}

// Resolve is the gateway's pre-call check. It never picks a fallback
// provider silently (§5.6).
func (s *store) Resolve(ctx context.Context, role Role) (ResolvedRole, error) {
	if _, ok := usage[role]; !ok {
		return ResolvedRole{}, fmt.Errorf("%w: %q", ErrUnknownRole, role)
	}
	a, found, err := s.assignment(ctx, role)
	if err != nil {
		return ResolvedRole{}, err
	}
	if !found {
		return ResolvedRole{}, &Error{Role: role, Problem: ProblemRoleUnassigned}
	}
	return s.resolve(ctx, role, a)
}

func (s *store) resolve(ctx context.Context, role Role, a RoleAssignment) (ResolvedRole, error) {
	required := usage[role]
	p, err := s.loadProvider(ctx, a.ProviderID)
	if err != nil {
		return ResolvedRole{}, err
	}
	if !p.Enabled {
		return ResolvedRole{}, &Error{Role: role, Problem: ProblemProviderDisabled, Detail: p.Name}
	}
	if !provider.Has(p.Protocol, required) {
		return ResolvedRole{}, &Error{Role: role, Problem: ProblemCapabilityMissing,
			Detail: fmt.Sprintf("protocol %s lacks %s", p.Protocol, required)}
	}
	m, err := s.loadModel(ctx, a.ProviderID, a.ModelID, required)
	if err != nil {
		var re *Error
		if errors.As(err, &re) && re.Role == "" {
			re.Role = role
			return ResolvedRole{}, re
		}
		return ResolvedRole{}, err
	}
	return ResolvedRole{Role: role, Capability: required, Provider: p, Model: m}, nil
}

func (s *store) assignment(ctx context.Context, role Role) (RoleAssignment, bool, error) {
	var (
		a         RoleAssignment
		updatedAt string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT role, provider_id, model_id, updated_at FROM role_assignments WHERE role = ?`,
		string(role)).Scan(&a.Role, &a.ProviderID, &a.ModelID, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, fmt.Errorf("role: load assignment: %w", err)
	}
	a.UpdatedAt, err = clock.Parse(updatedAt)
	if err != nil {
		return a, false, fmt.Errorf("role: updated_at: %w", err)
	}
	return a, true, nil
}

func (s *store) loadProvider(ctx context.Context, id string) (provider.ProviderConfig, error) {
	var (
		p       provider.ProviderConfig
		enabled int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, protocol, base_url, api_key_ref, enabled FROM providers WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Protocol, &p.BaseURL, &p.APIKeyRef, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("%w: %s", provider.ErrNotFound, id)
	}
	if err != nil {
		return p, fmt.Errorf("role: load provider: %w", err)
	}
	p.Enabled = enabled != 0
	return p, nil
}

// loadModel accepts manual rows always and listed rows only while available,
// and enforces the cached capability narrowing (§5.6 validation rule 4).
func (s *store) loadModel(ctx context.Context, providerID, modelID string, required provider.Capability) (model.ModelInfo, error) {
	var (
		m        model.ModelInfo
		source   string
		avail    int64
		capsJSON string
		last     string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT display_name, capabilities, source, available, last_seen_at
		 FROM provider_models WHERE provider_id = ? AND model_id = ?`,
		providerID, modelID).
		Scan(&m.DisplayName, &capsJSON, &source, &avail, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return m, &Error{Problem: ProblemModelUnavailable, Detail: "model " + modelID + " is not in the cache"}
	}
	if err != nil {
		return m, fmt.Errorf("role: load model: %w", err)
	}
	m.ProviderID = providerID
	m.ModelID = modelID
	m.Source = model.ModelSource(source)
	m.Available = avail != 0
	if err := json.Unmarshal([]byte(capsJSON), &m.Capabilities); err != nil {
		return m, fmt.Errorf("role: capabilities %s: %w", modelID, err)
	}
	if m.LastSeenAt, err = clock.Parse(last); err != nil {
		return m, fmt.Errorf("role: last_seen_at %s: %w", modelID, err)
	}
	if m.Source == model.SourceListed && !m.Available {
		return m, &Error{Problem: ProblemModelUnavailable, Detail: "model " + modelID + " is unavailable"}
	}
	if !capsAccept(m.Capabilities, required) {
		return m, &Error{Problem: ProblemCapabilityMissing,
			Detail: "model " + modelID + " does not offer " + string(required)}
	}
	return m, nil
}

func capsAccept(caps []provider.Capability, want provider.Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}
