package search

import (
	"context"
	"errors"
	"sync"
	"time"

	"researchos/internal/ai/gateway"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/workspace"
)

// Default tuning of the SPEC-10 pipeline (§5.2, §5.3).
const (
	defaultLimit          = 20
	maxLimit              = 100
	defaultKeywordTopK    = 50
	defaultVectorTopK     = 50
	defaultRRFK           = 60
	defaultRerankTopN     = 40
	defaultSnippetRunes   = 200
	defaultMaxAttempts    = 3
	defaultEmbedBatchSize = 32
	defaultDebounce       = 1500 * time.Millisecond
	defaultRerankTimeout  = 800 * time.Millisecond
	defaultProjectTimeout = 2 * time.Second
	defaultGlobalBudget   = 2500 * time.Millisecond
	defaultMaxConcurrency = 8
)

// Opener launches an external application for a path or URI (SPEC-10 §5.5
// O5). It defaults to the OS default opener; tests inject a recorder.
type Opener func(ctx context.Context, target string) error

// DeepLinkFunc tries an application-specific deep link (for example a Zotero
// reader page anchor). ok=false means the scheme is unavailable and the opener
// falls back to opening the PDF file; the caller still receives the page number
// on the JumpTarget (SPEC-10 §5.5, O5).
type DeepLinkFunc func(ctx context.Context, target JumpTarget) (ok bool, err error)

// Navigator performs in-app navigation for non-PDF targets. The backend keeps
// it as a port so the service layer stays UI-free; a nil navigator reports
// ErrOpenUnsupported.
type Navigator func(ctx context.Context, target JumpTarget) error

// Hooks is the observability seam of the index pipeline. All fields are
// optional and must not change behaviour.
type Hooks struct {
	// DocIndexed fires after one document projection was written.
	DocIndexed func(scopeKey, docID, status string)
	// JobFinished fires when a reindex job reaches a terminal state.
	JobFinished func(scopeKey, jobID, status string)
}

// Options configures a search Service. Zero values select the SPEC-10
// defaults; providers and the parser are optional and only enable upgrades
// (vector recall, rerank, real PDF parsing).
type Options struct {
	Clock  clock.Clock
	Logger workspace.Logger

	// Embedding enables vector recall and vector indexing. Nil means keyword
	// only, with vector_unavailable reported (SPEC-10 §5.1).
	Embedding gateway.EmbeddingService
	// Rerank enables the rerank stage. The gateway implementation currently
	// always returns unsupported_capability, which the service degrades to
	// rerank_unavailable without failing the search (SPEC-10 §5.2, ruling 4).
	Rerank gateway.RerankService

	// Parser overrides the pure-Go PDF parser. Nil uses GoPDFParser.
	Parser PDFParser
	// Opener opens PDFs externally. Nil uses the OS default opener.
	Opener Opener
	// DeepLink optionally opens a PDF at a page in an external reader.
	DeepLink  DeepLinkFunc
	Navigator Navigator

	Debounce       time.Duration
	KeywordTopK    int
	VectorTopK     int
	RerankTopN     int
	RRFK           int
	KeywordWeight  float64
	VectorWeight   float64
	RerankTimeout  time.Duration
	ProjectTimeout time.Duration
	GlobalBudget   time.Duration
	MaxConcurrency int
	MaxAttempts    int
	Backoff        []time.Duration
	EmbedBatchSize int
	SnippetRunes   int
	Limit          int // default page size
	MaxLimit       int
	Hooks          Hooks
	// ProjectBudget overrides the per-project timeout for one project. It is
	// the tuning seam used to keep federation latency bounded; nil uses
	// ProjectTimeout for every project. A non-positive value means "budget
	// exhausted" and deterministically produces project_timeout.
	ProjectBudget func(projectID string) time.Duration
}

// config is the normalized option set used by the service.
type config struct {
	Clock          clock.Clock
	Logger         workspace.Logger
	Embedding      gateway.EmbeddingService
	Rerank         gateway.RerankService
	parse          func(ctx context.Context, absPath string, opts ParseOptions) (PdfDocument, error)
	opener         Opener
	deepLink       DeepLinkFunc
	navigator      Navigator
	debounce       time.Duration
	keywordTopK    int
	vectorTopK     int
	rerankTopN     int
	rrfK           int
	keywordWeight  float64
	vectorWeight   float64
	rerankTimeout  time.Duration
	projectTimeout time.Duration
	globalBudget   time.Duration
	maxConcurrency int
	maxAttempts    int
	backoff        []time.Duration
	embedBatchSize int
	snippetRunes   int
	limit          int
	maxLimit       int
	hooks          Hooks
	projectBudget  func(projectID string) time.Duration
}

// withDefaults normalizes opt.
func (opt Options) withDefaults() config {
	c := opt.Clock
	if c == nil {
		c = clock.System{}
	}
	cfg := config{
		Clock:          c,
		Logger:         opt.Logger,
		Embedding:      opt.Embedding,
		Rerank:         opt.Rerank,
		opener:         opt.Opener,
		deepLink:       opt.DeepLink,
		navigator:      opt.Navigator,
		debounce:       opt.Debounce,
		keywordTopK:    opt.KeywordTopK,
		vectorTopK:     opt.VectorTopK,
		rerankTopN:     opt.RerankTopN,
		rrfK:           opt.RRFK,
		keywordWeight:  opt.KeywordWeight,
		vectorWeight:   opt.VectorWeight,
		rerankTimeout:  opt.RerankTimeout,
		projectTimeout: opt.ProjectTimeout,
		globalBudget:   opt.GlobalBudget,
		maxConcurrency: opt.MaxConcurrency,
		maxAttempts:    opt.MaxAttempts,
		backoff:        opt.Backoff,
		embedBatchSize: opt.EmbedBatchSize,
		snippetRunes:   opt.SnippetRunes,
		limit:          opt.Limit,
		maxLimit:       opt.MaxLimit,
		hooks:          opt.Hooks,
		projectBudget:  opt.ProjectBudget,
	}
	if cfg.debounce <= 0 {
		cfg.debounce = defaultDebounce
	}
	if cfg.keywordTopK <= 0 {
		cfg.keywordTopK = defaultKeywordTopK
	}
	if cfg.vectorTopK <= 0 {
		cfg.vectorTopK = defaultVectorTopK
	}
	if cfg.rerankTopN <= 0 {
		cfg.rerankTopN = defaultRerankTopN
	}
	if cfg.rrfK <= 0 {
		cfg.rrfK = defaultRRFK
	}
	if cfg.keywordWeight <= 0 {
		cfg.keywordWeight = 1
	}
	if cfg.vectorWeight <= 0 {
		cfg.vectorWeight = 1
	}
	if cfg.rerankTimeout <= 0 {
		cfg.rerankTimeout = defaultRerankTimeout
	}
	if cfg.projectTimeout <= 0 {
		cfg.projectTimeout = defaultProjectTimeout
	}
	if cfg.globalBudget <= 0 {
		cfg.globalBudget = defaultGlobalBudget
	}
	if cfg.maxConcurrency <= 0 {
		cfg.maxConcurrency = defaultMaxConcurrency
	}
	if cfg.maxAttempts <= 0 {
		cfg.maxAttempts = defaultMaxAttempts
	}
	if len(cfg.backoff) == 0 {
		cfg.backoff = []time.Duration{time.Second, 4 * time.Second, 16 * time.Second}
	}
	if cfg.embedBatchSize <= 0 {
		cfg.embedBatchSize = defaultEmbedBatchSize
	}
	if cfg.snippetRunes <= 0 {
		cfg.snippetRunes = defaultSnippetRunes
	}
	if cfg.limit <= 0 {
		cfg.limit = defaultLimit
	}
	if cfg.maxLimit <= 0 {
		cfg.maxLimit = maxLimit
	}
	if cfg.parse == nil {
		if opt.Parser != nil {
			cfg.parse = opt.Parser.Parse
		} else {
			cfg.parse = ParsePDFFile
		}
	}
	if cfg.opener == nil {
		cfg.opener = SystemOpener
	}
	return cfg
}

// Service implements the SPEC-10 search service, the index coordinator port
// and the foundation indexhook.Indexer. One Service serves every scope of one
// workspace; use ProjectIndexer(id) to hand a project-scoped indexer to the
// entity services.
type Service struct {
	ws  *workspace.Workspace
	cfg config
	ex  *extractor

	mu      sync.Mutex
	runners map[string]*runner
	closed  bool
	baseCtx context.Context
	cancel  context.CancelFunc
}

// New binds a search service to an open workspace.
func New(ws *workspace.Workspace, opt Options) (*Service, error) {
	if ws == nil {
		return nil, errors.New("search: workspace is required")
	}
	cfg := opt.withDefaults()
	if cfg.Logger == nil {
		cfg.Logger = slogDiscard{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		ws:      ws,
		cfg:     cfg,
		ex:      &extractor{ws: ws, clock: cfg.Clock, parse: cfg.parse},
		runners: make(map[string]*runner),
		baseCtx: ctx,
		cancel:  cancel,
	}, nil
}

// Close stops the background index workers. It is idempotent.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	runners := make([]*runner, 0, len(s.runners))
	for _, r := range s.runners {
		runners = append(runners, r)
	}
	s.mu.Unlock()
	s.cancel()
	for _, r := range runners {
		r.stopOnce()
	}
	return nil
}

// slogDiscard is the silent logger used when none is injected.
type slogDiscard struct{}

// Debug implements workspace.Logger.
func (slogDiscard) Debug(string, ...any) {}

// Info implements workspace.Logger.
func (slogDiscard) Info(string, ...any) {}

// Warn implements workspace.Logger.
func (slogDiscard) Warn(string, ...any) {}

// Error implements workspace.Logger.
func (slogDiscard) Error(string, ...any) {}

// notifyDocIndexed fires the optional observability hook.
func (s *Service) notifyDocIndexed(scopeKey, docID, status string) {
	if s.cfg.hooks.DocIndexed != nil {
		s.cfg.hooks.DocIndexed(scopeKey, docID, status)
	}
}
