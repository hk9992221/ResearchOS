// Package model caches upstream model listings per provider and manages
// manual models (SPEC-02 §5.5). Listed rows are marked unavailable instead of
// deleted so role assignments never dangle; manual rows survive every refresh.
package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/secret"
	"researchos/internal/foundation/clock"
)

// ModelSource distinguishes cached-upstream from hand-entered rows (§4.2).
type ModelSource string

// Registered sources.
const (
	SourceListed ModelSource = "listed"
	SourceManual ModelSource = "manual"
)

// Sentinel errors.
var (
	ErrModelNotFound = errors.New("model not found in cache")
	ErrModelExists   = errors.New("model already exists for this provider")
	ErrNotManual     = errors.New("only manual models can be removed")
	ErrEmptyModelID  = errors.New("model id must not be empty")
)

// InUseError reports a removal attempt blocked by role assignments (§5.5.4).
type InUseError struct {
	ProviderID string
	ModelID    string
	Roles      []string
}

func (e *InUseError) Error() string {
	return "model: in use by roles: " + strings.Join(e.Roles, ", ")
}

// ModelInfo is one cached model row.
type ModelInfo struct {
	ProviderID   string
	ModelID      string
	DisplayName  string
	Capabilities []provider.Capability
	Source       ModelSource
	Available    bool
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
}

// ModelSyncResult summarizes one manual refresh (§5.5).
type ModelSyncResult struct {
	Added             int
	MarkedUnavailable int
	Total             int
	FetchedAt         time.Time
	Warning           string
}

// ModelQuery filters the cache; ProviderID is required.
type ModelQuery struct {
	ProviderID         string
	Capability         provider.Capability
	IncludeUnavailable bool
}

// Service is the ModelService business entry.
type Service interface {
	Sync(ctx context.Context, providerID string) (ModelSyncResult, error)
	List(ctx context.Context, q ModelQuery) ([]ModelInfo, error)
	AddManual(ctx context.Context, providerID, modelID, displayName string) error
	RemoveManual(ctx context.Context, providerID, modelID string) error
}

// Options wires the model cache.
type Options struct {
	Clock   clock.Clock
	Secrets secret.Store
	Lister  provider.ModelLister
	// ListTimeout bounds one upstream listing; zero uses provider.TimeoutList.
	ListTimeout time.Duration
}

type store struct {
	db    *sql.DB
	clock clock.Clock
	opts  Options
}

var _ Service = (*store)(nil)

// New returns the model service over global.db.
func New(db *sql.DB, opts Options) (Service, error) {
	if db == nil {
		return nil, errors.New("model: nil database")
	}
	if opts.Secrets == nil || opts.Lister == nil {
		return nil, errors.New("model: secrets store and lister are required")
	}
	c := opts.Clock
	if c == nil {
		c = clock.System{}
	}
	if opts.ListTimeout <= 0 {
		opts.ListTimeout = provider.TimeoutList
	}
	return &store{db: db, clock: c, opts: opts}, nil
}

// Sync fetches the upstream list and reconciles the cache. On failure the
// cache and availability stay untouched; only models_fetch_error records the
// sanitized message and the error is returned (§5.5.2, AC-03.2).
func (s *store) Sync(ctx context.Context, providerID string) (ModelSyncResult, error) {
	proto, ref, baseURL, err := s.loadProvider(ctx, providerID)
	if err != nil {
		return ModelSyncResult{}, err
	}
	var key provider.Secret
	if ref != "" {
		raw, err := s.opts.Secrets.Get(ctx, ref)
		if err != nil {
			s.recordFetchError(ctx, providerID, "secret_unavailable: "+aierr.Truncate(err.Error()))
			return ModelSyncResult{}, fmt.Errorf("model: read key: %w", err)
		}
		key = provider.Secret(raw)
	}

	callCtx, cancel := context.WithTimeout(ctx, s.opts.ListTimeout)
	defer cancel()
	cfg := provider.ProviderConfig{ID: providerID, Protocol: proto, BaseURL: baseURL, APIKeyRef: ref}
	upstream, err := s.opts.Lister.ListModels(callCtx, cfg, key)
	if err != nil {
		s.recordFetchError(ctx, providerID, fetchErrorText(err, key))
		return ModelSyncResult{}, err
	}

	now := s.clock.Now()
	res, err := s.syncTx(ctx, providerID, proto, upstream, now)
	if err != nil {
		return res, err
	}
	res.FetchedAt = now
	if res.Total == 0 {
		res.Warning = "上游返回空模型列表"
	}
	return res, nil
}

// syncTx applies one successful listing atomically and reports the deltas.
func (s *store) syncTx(ctx context.Context, providerID string, proto provider.Protocol, upstream []provider.UpstreamModel, now time.Time) (ModelSyncResult, error) {
	var res ModelSyncResult
	nowStr := clock.Format(now)
	existing, err := s.existingRows(ctx, providerID)
	if err != nil {
		return res, err
	}
	seen := map[string]bool{}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("model: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, m := range upstream {
		if m.ModelID == "" || seen[m.ModelID] {
			continue
		}
		seen[m.ModelID] = true
		res.Total++
		caps := resolveCaps(proto, m.Capabilities)
		capsJSON, err := json.Marshal(caps)
		if err != nil {
			return res, err
		}
		display := m.DisplayName
		if display == "" {
			display = m.ModelID
		}
		prev, existed := existing[m.ModelID]
		switch {
		case !existed:
			_, err = tx.ExecContext(ctx,
				`INSERT INTO provider_models (provider_id, model_id, display_name, capabilities,
					source, available, first_seen_at, last_seen_at)
				 VALUES (?, ?, ?, ?, 'listed', 1, ?, ?)`,
				providerID, m.ModelID, display, string(capsJSON), nowStr, nowStr)
			res.Added++
		case prev.source == SourceManual:
			// Manual rows keep their source; a refresh only confirms presence.
			_, err = tx.ExecContext(ctx,
				`UPDATE provider_models SET available = 1, last_seen_at = ?
				 WHERE provider_id = ? AND model_id = ?`, nowStr, providerID, m.ModelID)
		default:
			_, err = tx.ExecContext(ctx,
				`UPDATE provider_models SET display_name = ?, capabilities = ?,
					available = 1, last_seen_at = ?
				 WHERE provider_id = ? AND model_id = ?`,
				display, string(capsJSON), nowStr, providerID, m.ModelID)
		}
		if err != nil {
			return res, fmt.Errorf("model: upsert %s: %w", m.ModelID, err)
		}
	}

	// Listed rows the upstream no longer reports become unavailable; manual
	// rows never do (§5.5.1, AC-03.3).
	for id, e := range existing {
		if e.source == SourceListed && e.available && !seen[id] {
			if _, err := tx.ExecContext(ctx,
				`UPDATE provider_models SET available = 0 WHERE provider_id = ? AND model_id = ?`,
				providerID, id); err != nil {
				return res, err
			}
			res.MarkedUnavailable++
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE providers SET models_fetched_at = ?, models_fetch_error = NULL WHERE id = ?`,
		nowStr, providerID); err != nil {
		return res, fmt.Errorf("model: stamp fetch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("model: commit: %w", err)
	}
	return res, nil
}

type cachedRow struct {
	source    ModelSource
	available bool
}

func (s *store) existingRows(ctx context.Context, providerID string) (map[string]cachedRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model_id, source, available FROM provider_models WHERE provider_id = ?`, providerID)
	if err != nil {
		return nil, fmt.Errorf("model: existing rows: %w", err)
	}
	defer rows.Close()
	out := map[string]cachedRow{}
	for rows.Next() {
		var (
			id     string
			source string
			avail  int64
		)
		if err := rows.Scan(&id, &source, &avail); err != nil {
			return nil, err
		}
		out[id] = cachedRow{source: ModelSource(source), available: avail != 0}
	}
	return out, rows.Err()
}

// resolveCaps narrows the protocol capability set with explicit upstream
// metadata; upstream data can only narrow, never widen (§3.2 rule 1).
func resolveCaps(proto provider.Protocol, upstream []provider.Capability) []provider.Capability {
	if len(upstream) == 0 {
		return provider.CapabilitiesList(proto)
	}
	var out []provider.Capability
	for _, c := range upstream {
		if provider.Has(proto, c) {
			out = append(out, c)
		}
	}
	return out
}

func (s *store) loadProvider(ctx context.Context, id string) (proto provider.Protocol, ref, baseURL string, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT protocol, api_key_ref, base_url FROM providers WHERE id = ?`, id).
		Scan(&proto, &ref, &baseURL)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", fmt.Errorf("%w: %s", provider.ErrNotFound, id)
	}
	if err != nil {
		return "", "", "", fmt.Errorf("model: load provider: %w", err)
	}
	return proto, ref, baseURL, nil
}

// recordFetchError persists the sanitized last error; the UI keeps showing
// the old cache alongside it (§5.5.2).
func (s *store) recordFetchError(ctx context.Context, providerID, message string) {
	_, _ = s.db.ExecContext(ctx,
		`UPDATE providers SET models_fetch_error = ? WHERE id = ?`, message, providerID)
}

func fetchErrorText(err error, key provider.Secret) string {
	var ae *aierr.AIError
	if errors.As(err, &ae) {
		return string(ae.Kind) + "|" + aierr.RedactWith(ae.Message, key)
	}
	return aierr.RedactWith(err.Error(), key)
}

// List returns cached models for one provider, filtered by capability; the
// protocol floor always applies on top (§5.5.5).
func (s *store) List(ctx context.Context, q ModelQuery) ([]ModelInfo, error) {
	if q.ProviderID == "" {
		return nil, errors.New("model: ProviderID is required")
	}
	proto, err := s.providerProtocol(ctx, q.ProviderID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT model_id, display_name, capabilities, source, available, first_seen_at, last_seen_at
		 FROM provider_models WHERE provider_id = ? ORDER BY model_id`, q.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("model: list: %w", err)
	}
	defer rows.Close()
	var out []ModelInfo
	for rows.Next() {
		mi, err := scanModel(rows, q.ProviderID)
		if err != nil {
			return nil, err
		}
		if !q.IncludeUnavailable && !mi.Available {
			continue
		}
		if q.Capability != "" {
			if !containsCap(mi.Capabilities, q.Capability) || !provider.Has(proto, q.Capability) {
				continue
			}
		}
		out = append(out, mi)
	}
	return out, rows.Err()
}

func (s *store) providerProtocol(ctx context.Context, id string) (provider.Protocol, error) {
	var proto provider.Protocol
	err := s.db.QueryRowContext(ctx, `SELECT protocol FROM providers WHERE id = ?`, id).Scan(&proto)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", provider.ErrNotFound, id)
	}
	return proto, err
}

func scanModel(rows *sql.Rows, providerID string) (ModelInfo, error) {
	var (
		mi          ModelInfo
		capsJSON    string
		source      string
		avail       int64
		first, last string
	)
	if err := rows.Scan(&mi.ModelID, &mi.DisplayName, &capsJSON, &source, &avail, &first, &last); err != nil {
		return mi, err
	}
	mi.ProviderID = providerID
	mi.Source = ModelSource(source)
	mi.Available = avail != 0
	if err := json.Unmarshal([]byte(capsJSON), &mi.Capabilities); err != nil {
		return mi, fmt.Errorf("model: capabilities %s: %w", mi.ModelID, err)
	}
	var err error
	if mi.FirstSeenAt, err = clock.Parse(first); err != nil {
		return mi, err
	}
	if mi.LastSeenAt, err = clock.Parse(last); err != nil {
		return mi, err
	}
	return mi, nil
}

func containsCap(caps []provider.Capability, want provider.Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// AddManual stores a hand-entered model; capabilities follow the protocol
// because per-model metadata is unavailable for relays (§5.5.3).
func (s *store) AddManual(ctx context.Context, providerID, modelID, displayName string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ErrEmptyModelID
	}
	proto, err := s.providerProtocol(ctx, providerID)
	if err != nil {
		return err
	}
	var exists int
	err = s.db.QueryRowContext(ctx,
		`SELECT 1 FROM provider_models WHERE provider_id = ? AND model_id = ?`,
		providerID, modelID).Scan(&exists)
	if err == nil {
		return ErrModelExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("model: exists check: %w", err)
	}
	capsJSON, err := json.Marshal(provider.CapabilitiesList(proto))
	if err != nil {
		return err
	}
	if displayName == "" {
		displayName = modelID
	}
	nowStr := clock.Format(s.clock.Now())
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO provider_models (provider_id, model_id, display_name, capabilities,
			source, available, first_seen_at, last_seen_at)
		 VALUES (?, ?, ?, ?, 'manual', 1, ?, ?)`,
		providerID, modelID, displayName, string(capsJSON), nowStr, nowStr)
	if err != nil {
		return fmt.Errorf("model: add manual: %w", err)
	}
	return nil
}

// RemoveManual deletes a hand-entered model unless a role references it;
// listed rows are cache state and are never removed here (§5.5.4).
func (s *store) RemoveManual(ctx context.Context, providerID, modelID string) error {
	var source string
	err := s.db.QueryRowContext(ctx,
		`SELECT source FROM provider_models WHERE provider_id = ? AND model_id = ?`,
		providerID, modelID).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrModelNotFound
	}
	if err != nil {
		return fmt.Errorf("model: load row: %w", err)
	}
	if ModelSource(source) != SourceManual {
		return ErrNotManual
	}
	roles, err := s.rolesUsing(ctx, providerID, modelID)
	if err != nil {
		return err
	}
	if len(roles) > 0 {
		return &InUseError{ProviderID: providerID, ModelID: modelID, Roles: roles}
	}
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM provider_models WHERE provider_id = ? AND model_id = ?`, providerID, modelID)
	if err != nil {
		return fmt.Errorf("model: remove manual: %w", err)
	}
	return nil
}

func (s *store) rolesUsing(ctx context.Context, providerID, modelID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT role FROM role_assignments WHERE provider_id = ? AND model_id = ? ORDER BY role`,
		providerID, modelID)
	if err != nil {
		return nil, fmt.Errorf("model: roles lookup: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
