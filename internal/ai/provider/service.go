package provider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"researchos/internal/ai/aierr"
	"researchos/internal/ai/secret"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/ids"
)

// Options wires the provider service to its seams.
type Options struct {
	// Clock supplies timestamps and IDs. Nil means clock.System.
	Clock clock.Clock
	// Secrets is the OS key store. Required (release: secret.NewOS()).
	Secrets secret.Store
	// Lister builds upstream model listings for Test. Nil makes Test fail
	// with ErrListerUnavailable; CRUD keeps working.
	Lister ModelLister
	// RoleCaps maps a role id to its required capability. It is injected from
	// the role package at wiring time so provider never imports role.
	// Unknown roles are skipped in protocol-change validation.
	RoleCaps func(role string) (Capability, bool)
	// TestTimeout overrides TimeoutList for connection tests. Zero uses the
	// default.
	TestTimeout time.Duration
}

// ErrListerUnavailable reports Test without a protocol factory wired in.
var ErrListerUnavailable = errors.New("provider: model lister not configured")

// store is the SQLite-backed ProviderService.
type store struct {
	db    *sql.DB
	clock clock.Clock
	opts  Options
}

var _ Service = (*store)(nil)

// New returns the provider service over global.db.
func New(db *sql.DB, opts Options) (Service, error) {
	if db == nil {
		return nil, errors.New("provider: nil database")
	}
	if opts.Secrets == nil {
		return nil, errors.New("provider: secret store is required")
	}
	c := opts.Clock
	if c == nil {
		c = clock.System{}
	}
	if opts.TestTimeout <= 0 {
		opts.TestTimeout = TimeoutList
	}
	return &store{db: db, clock: c, opts: opts}, nil
}

const providerColumns = `id, name, protocol, base_url, api_key_ref, enabled,
	created_at, updated_at, last_tested_at, last_test_ok, last_test_latency_ms,
	last_test_model_count, last_test_error, models_fetched_at, models_fetch_error`

type providerRow struct {
	cfg                  ProviderConfig
	CreatedAt, UpdatedAt time.Time

	LastTestedAt    *time.Time
	LastTestOK      *bool
	LastTestLatency *int64
	LastTestCount   *int
	LastTestError   *string
	ModelsFetchedAt *time.Time
	ModelsError     string
}

func scanProviderRow(row interface{ Scan(...any) error }) (providerRow, error) {
	var (
		p                providerRow
		enabled          int64
		created, updated string
		testedAt         sql.NullString
		testOK           sql.NullInt64
		testLat          sql.NullInt64
		testCnt          sql.NullInt64
		testErr          sql.NullString
		fetchedAt        sql.NullString
		modelsErr        sql.NullString
	)
	err := row.Scan(&p.cfg.ID, &p.cfg.Name, &p.cfg.Protocol, &p.cfg.BaseURL,
		&p.cfg.APIKeyRef, &enabled, &created, &updated,
		&testedAt, &testOK, &testLat, &testCnt, &testErr, &fetchedAt, &modelsErr)
	if err != nil {
		return p, err
	}
	p.cfg.Enabled = enabled != 0
	if p.CreatedAt, err = clock.Parse(created); err != nil {
		return p, fmt.Errorf("provider: %s created_at: %w", p.cfg.ID, err)
	}
	if p.UpdatedAt, err = clock.Parse(updated); err != nil {
		return p, fmt.Errorf("provider: %s updated_at: %w", p.cfg.ID, err)
	}
	if testedAt.Valid {
		t, err := clock.Parse(testedAt.String)
		if err != nil {
			return p, fmt.Errorf("provider: %s last_tested_at: %w", p.cfg.ID, err)
		}
		p.LastTestedAt = &t
	}
	if testOK.Valid {
		v := testOK.Int64 != 0
		p.LastTestOK = &v
	}
	if testLat.Valid {
		v := testLat.Int64
		p.LastTestLatency = &v
	}
	if testCnt.Valid {
		v := int(testCnt.Int64)
		p.LastTestCount = &v
	}
	if testErr.Valid {
		v := testErr.String
		p.LastTestError = &v
	}
	if fetchedAt.Valid {
		t, err := clock.Parse(fetchedAt.String)
		if err != nil {
			return p, fmt.Errorf("provider: %s models_fetched_at: %w", p.cfg.ID, err)
		}
		p.ModelsFetchedAt = &t
	}
	p.ModelsError = modelsErr.String
	return p, nil
}

func (p providerRow) view() ProviderView {
	v := ProviderView{
		ID:              p.cfg.ID,
		Name:            p.cfg.Name,
		Protocol:        p.cfg.Protocol,
		BaseURL:         p.cfg.BaseURL,
		HasKey:          p.cfg.HasKey(),
		Enabled:         p.cfg.Enabled,
		ModelsFetchedAt: p.ModelsFetchedAt,
		ModelsError:     p.ModelsError,
		CreatedAt:       p.CreatedAt,
		UpdatedAt:       p.UpdatedAt,
	}
	if p.LastTestedAt != nil {
		v.LastTest = &TestResult{TestedAt: *p.LastTestedAt}
		if p.LastTestOK != nil {
			v.LastTest.OK = *p.LastTestOK
		}
		if p.LastTestLatency != nil {
			v.LastTest.LatencyMS = *p.LastTestLatency
		}
		if p.LastTestCount != nil {
			v.LastTest.ModelCount = *p.LastTestCount
		}
		if p.LastTestError != nil {
			// The persisted form is "kind|message"; split for display.
			kind, msg := splitTestError(*p.LastTestError)
			v.LastTest.ErrorKind = kind
			v.LastTest.Message = msg
		}
	}
	return v
}

func splitTestError(s string) (kind, message string) {
	if i := strings.IndexByte(s, '|'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

func joinTestError(kind, message string) string {
	if kind == "" {
		return message
	}
	return kind + "|" + message
}

func (s *store) load(ctx context.Context, id string) (providerRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+providerColumns+` FROM providers WHERE id = ?`, id)
	p, err := scanProviderRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return p, fmt.Errorf("provider: load %s: %w", id, err)
	}
	return p, nil
}

// List returns every provider view in creation order.
func (s *store) List(ctx context.Context) ([]ProviderView, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+providerColumns+` FROM providers ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("provider: list: %w", err)
	}
	defer rows.Close()
	var out []ProviderView
	for rows.Next() {
		p, err := scanProviderRow(rows)
		if err != nil {
			return nil, fmt.Errorf("provider: list scan: %w", err)
		}
		out = append(out, p.view())
	}
	return out, rows.Err()
}

// Get returns one provider view.
func (s *store) Get(ctx context.Context, id string) (ProviderView, error) {
	p, err := s.load(ctx, id)
	if err != nil {
		return ProviderView{}, err
	}
	return p.view(), nil
}

// Create validates the input, writes the key before the config row and
// compensates when the row insert fails (SPEC-02 §5.3.1).
func (s *store) Create(ctx context.Context, in CreateProviderInput) (ProviderView, error) {
	if in.Name == "" {
		return ProviderView{}, ErrInvalidName
	}
	if !ValidProtocol(in.Protocol) {
		return ProviderView{}, fmt.Errorf("%w: %q", ErrInvalidProtocol, in.Protocol)
	}
	baseURL, err := NormalizeBaseURL(in.BaseURL)
	if err != nil {
		return ProviderView{}, err
	}
	id, err := ids.New(s.clock)
	if err != nil {
		return ProviderView{}, fmt.Errorf("provider: new id: %w", err)
	}
	ref := ""
	if in.APIKey != "" {
		ref = secret.ProviderKeyRef(id)
		if err := s.opts.Secrets.Set(ctx, ref, []byte(in.APIKey)); err != nil {
			return ProviderView{}, fmt.Errorf("provider: store key: %w", err)
		}
	}
	now := clock.Format(s.clock.Now())
	enabled := 0
	if in.Enabled {
		enabled = 1
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO providers (id, name, protocol, base_url, api_key_ref, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.Name, string(in.Protocol), baseURL, ref, enabled, now, now)
	if err != nil {
		if ref != "" {
			// Compensate: never leave an orphan secret behind (§4.5).
			_ = s.opts.Secrets.Delete(ctx, ref)
		}
		return ProviderView{}, fmt.Errorf("provider: insert: %w", err)
	}
	return s.Get(ctx, id)
}

// Update applies a patch with the full §5.3.2 rule set.
func (s *store) Update(ctx context.Context, id string, in UpdateProviderInput) (ProviderView, error) {
	p, err := s.load(ctx, id)
	if err != nil {
		return ProviderView{}, err
	}
	cfg := p.cfg

	if in.Name != nil {
		if *in.Name == "" {
			return ProviderView{}, ErrInvalidName
		}
		cfg.Name = *in.Name
	}
	if in.APIKey != nil && in.ClearKey {
		return ProviderView{}, ErrKeyConflict
	}

	protocolChanged := false
	if in.Protocol != nil && *in.Protocol != cfg.Protocol {
		if !ValidProtocol(*in.Protocol) {
			return ProviderView{}, fmt.Errorf("%w: %q", ErrInvalidProtocol, *in.Protocol)
		}
		if err := s.checkProtocolRoles(ctx, cfg.ID, *in.Protocol); err != nil {
			return ProviderView{}, err
		}
		cfg.Protocol = *in.Protocol
		protocolChanged = true
	}
	baseChanged := false
	if in.BaseURL != nil {
		norm, err := NormalizeBaseURL(*in.BaseURL)
		if err != nil {
			return ProviderView{}, err
		}
		if norm != cfg.BaseURL {
			cfg.BaseURL = norm
			baseChanged = true
		}
	}
	if protocolChanged || baseChanged {
		if err := s.resetModelCaches(ctx, cfg.ID, cfg.Protocol); err != nil {
			return ProviderView{}, err
		}
	}

	enabled := cfg.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	// Key mutations happen against the OS store first; the row is written only
	// after the secret operation succeeded (§4.5 order, §5.3.2).
	ref := cfg.APIKeyRef
	if in.ClearKey && ref != "" {
		if err := s.opts.Secrets.Delete(ctx, ref); err != nil {
			return ProviderView{}, fmt.Errorf("provider: clear key: %w", err)
		}
		ref = ""
	}
	if in.APIKey != nil {
		if *in.APIKey == "" {
			if ref != "" {
				if err := s.opts.Secrets.Delete(ctx, ref); err != nil {
					return ProviderView{}, fmt.Errorf("provider: clear key: %w", err)
				}
			}
			ref = ""
		} else {
			if ref == "" {
				ref = secret.ProviderKeyRef(cfg.ID)
			}
			if err := s.opts.Secrets.Set(ctx, ref, []byte(*in.APIKey)); err != nil {
				return ProviderView{}, fmt.Errorf("provider: store key: %w", err)
			}
		}
	}
	cfg.APIKeyRef = ref

	if enabled && !cfg.Enabled {
		if err := s.checkSecretReadable(ctx, cfg); err != nil {
			return ProviderView{}, err
		}
	}

	now := clock.Format(s.clock.Now())
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE providers SET name = ?, protocol = ?, base_url = ?, api_key_ref = ?,
			enabled = ?, updated_at = ? WHERE id = ?`,
		cfg.Name, string(cfg.Protocol), cfg.BaseURL, cfg.APIKeyRef, enabledInt, now, cfg.ID)
	if err != nil {
		return ProviderView{}, fmt.Errorf("provider: update: %w", err)
	}
	return s.Get(ctx, id)
}

// checkProtocolRoles rejects a protocol change that would leave assigned
// roles without their required capability (§5.3.2 rule 3).
func (s *store) checkProtocolRoles(ctx context.Context, id string, next Protocol) error {
	if s.opts.RoleCaps == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT role FROM role_assignments WHERE provider_id = ?`, id)
	if err != nil {
		return fmt.Errorf("provider: role lookup: %w", err)
	}
	defer rows.Close()
	var conflicts []RoleConflict
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return fmt.Errorf("provider: role scan: %w", err)
		}
		req, ok := s.opts.RoleCaps(role)
		if !ok {
			continue
		}
		if !Has(next, req) {
			conflicts = append(conflicts, RoleConflict{Role: role, Required: req})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("provider: role rows: %w", err)
	}
	if len(conflicts) > 0 {
		return &ProtocolConflictError{ProviderID: id, Conflicts: conflicts}
	}
	return nil
}

// resetModelCaches drops listed models, recomputes manual capabilities under
// the new protocol and clears test/fetch state (§5.3.2).
func (s *store) resetModelCaches(ctx context.Context, id string, next Protocol) error {
	capsJSON, err := CapabilitiesJSON(next)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM provider_models WHERE provider_id = ? AND source = 'listed'`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE provider_models SET capabilities = ? WHERE provider_id = ? AND source = 'manual'`,
			capsJSON, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE providers SET models_fetched_at = NULL, models_fetch_error = NULL,
				last_tested_at = NULL, last_test_ok = NULL, last_test_latency_ms = NULL,
				last_test_model_count = NULL, last_test_error = NULL
			 WHERE id = ?`, id)
		return err
	})
}

// SetEnabled enables or disables a provider; enabling a key-requiring
// provider verifies the secret is readable (§5.3.4).
func (s *store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	p, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if enabled && !p.cfg.Enabled {
		if err := s.checkSecretReadable(ctx, p.cfg); err != nil {
			return err
		}
	}
	v := 0
	if enabled {
		v = 1
	}
	now := clock.Format(s.clock.Now())
	_, err = s.db.ExecContext(ctx,
		`UPDATE providers SET enabled = ?, updated_at = ? WHERE id = ?`, v, now, id)
	if err != nil {
		return fmt.Errorf("provider: set enabled: %w", err)
	}
	return nil
}

// checkSecretReadable enforces §5.3.4: a provider with a key reference may
// only be enabled while the OS store can read the entry. Keyless services
// (empty ref) are exempt.
func (s *store) checkSecretReadable(ctx context.Context, cfg ProviderConfig) error {
	if cfg.APIKeyRef == "" {
		return nil
	}
	_, err := s.opts.Secrets.Get(ctx, cfg.APIKeyRef)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, secret.ErrSecretStoreUnavailable):
		return fmt.Errorf("%w: %v", ErrSecretStoreGone, err)
	default:
		// Missing entry or store outage both reject the enable (§4.5).
		return fmt.Errorf("%w: %v", ErrSecretStoreGone, err)
	}
}

// Delete removes a provider after confirming no role references it: key
// first, then models cache and config row (§5.3.3).
func (s *store) Delete(ctx context.Context, id string) error {
	p, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	roles, err := s.rolesFor(ctx, id)
	if err != nil {
		return err
	}
	if len(roles) > 0 {
		return &InUseError{ProviderID: id, Roles: roles}
	}
	if p.cfg.APIKeyRef != "" {
		if err := s.opts.Secrets.Delete(ctx, p.cfg.APIKeyRef); err != nil {
			// Abort and keep the configuration: a lingering key without its
			// provider is the worse failure (§4.5).
			return fmt.Errorf("provider: delete key: %w", err)
		}
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM provider_models WHERE provider_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("provider: delete: %w", err)
	}
	return nil
}

// rolesFor lists the role ids assigned to this provider.
func (s *store) rolesFor(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT role FROM role_assignments WHERE provider_id = ? ORDER BY role`, id)
	if err != nil {
		return nil, fmt.Errorf("provider: roles lookup: %w", err)
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

// Test probes the provider by listing upstream models without consuming
// generation tokens and without writing the model cache (§5.3.5).
func (s *store) Test(ctx context.Context, id string) (TestResult, error) {
	p, err := s.load(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	res := TestResult{TestedAt: s.clock.Now()}

	var key Secret
	if p.cfg.APIKeyRef != "" {
		raw, err := s.opts.Secrets.Get(ctx, p.cfg.APIKeyRef)
		if err != nil {
			kind := string(aierr.ErrKindSecretUnavailable)
			res.ErrorKind, res.Message = kind, aierr.Truncate(err.Error())
			_ = s.saveTestResult(ctx, id, res)
			return res, nil
		}
		key = Secret(raw)
	}
	if s.opts.Lister == nil {
		res.ErrorKind = string(aierr.ErrKindUnsupported)
		res.Message = ErrListerUnavailable.Error()
		_ = s.saveTestResult(ctx, id, res)
		return res, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, s.opts.TestTimeout)
	defer cancel()
	start := s.clock.Now()
	models, err := s.opts.Lister.ListModels(callCtx, p.cfg, key)
	res.LatencyMS = durationMS(s.clock.Now().Sub(start))
	if err != nil {
		res.ErrorKind, res.Message = classify(err, key)
		// A relay without a /models endpoint is a listing-capability gap, not
		// a missing model (§5.3.5, AC-03). The provider stays saveable.
		switch res.ErrorKind {
		case string(aierr.ErrKindModelNotFound), string(aierr.ErrKindInvalidRequest):
			res.ErrorKind = string(aierr.ErrKindUnsupported)
			res.Message = "列表接口不可用：" + res.Message
		}
		if p.cfg.APIKeyRef == "" && res.ErrorKind == string(aierr.ErrKindAuth) {
			// Keyless services surfacing 401: the upstream demands auth; say
			// so in plain terms (§5.3.5 中转不支持 /models 等错误可见).
			res.Message = "上游要求鉴权，但当前配置为无密钥服务：" + res.Message
		}
	} else {
		res.OK = true
		res.ModelCount = len(models)
	}
	if err := s.saveTestResult(ctx, id, res); err != nil {
		return res, err
	}
	return res, nil
}

func (s *store) saveTestResult(ctx context.Context, id string, res TestResult) error {
	var testedOK any
	if res.OK {
		testedOK = 1
	} else if res.ErrorKind != "" {
		testedOK = 0
	} else {
		testedOK = nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE providers SET last_tested_at = ?, last_test_ok = ?, last_test_latency_ms = ?,
			last_test_model_count = ?, last_test_error = ? WHERE id = ?`,
		clock.Format(res.TestedAt), testedOK, res.LatencyMS, res.ModelCount,
		testErrorMessage(res), id)
	if err != nil {
		return fmt.Errorf("provider: save test result: %w", err)
	}
	return nil
}

func testErrorMessage(res TestResult) any {
	if res.OK {
		return nil
	}
	return joinTestError(res.ErrorKind, aierr.Truncate(res.Message))
}

// classify maps a transport error onto (kind, sanitized message).
func classify(err error, key Secret) (string, string) {
	var ae *aierr.AIError
	if errors.As(err, &ae) {
		return string(ae.Kind), aierr.RedactWith(ae.Message, key)
	}
	return string(aierr.ErrKindNetwork), aierr.RedactWith(err.Error(), key)
}

func durationMS(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d / time.Millisecond)
}

func (s *store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
