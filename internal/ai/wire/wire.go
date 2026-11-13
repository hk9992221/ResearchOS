// Package wire assembles the SPEC-02 service stack over an open workspace:
// provider/model/role services, the gateway and the module-facing facades,
// with the SPEC-02 migrations available for workspace.Options.
//
// It exists so UI (P12/P13) and MCP (SPEC-11) share one construction path
// and cannot drift apart (RQ-17, SPEC-00 §12).
package wire

import (
	"database/sql"
	"errors"
	"net/http"

	"researchos/internal/ai/gateway"
	"researchos/internal/ai/migrations"
	"researchos/internal/ai/model"
	"researchos/internal/ai/protocol"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
	"researchos/internal/ai/secret"
	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/storage"
)

// Stack holds the SPEC-02 services wired to one workspace.
type Stack struct {
	Providers provider.Service
	Models    model.Service
	Roles     role.Service
	Gateway   *gateway.Service
	Chat      gateway.ChatService
	Embedding gateway.EmbeddingService
	Rerank    gateway.RerankService
}

// Options describes the workspace handles the stack binds to.
type Options struct {
	// Global is global.db (providers, provider_models, role_assignments).
	Global *sql.DB
	Clock  clock.Clock
	// Secrets defaults to secret.NewOS() when nil. Tests inject
	// secret.Memory; release builds must not.
	Secrets secret.Store
	// HTTPClient overrides the transport used by protocol adapters.
	HTTPClient *http.Client
	// WorkspaceSettings caches observed embedding dimensions; nil disables
	// the CurrentProfile dimension hint.
	WorkspaceSettings *settings.Store
	Logger            gateway.Logger
}

// Migrations returns the SPEC-02 band to pass as workspace.Options.
// Migrations.
func Migrations() (map[storage.Kind][]storage.Migration, error) {
	return migrations.All()
}

// New constructs the full service stack.
func New(opt Options) (*Stack, error) {
	if opt.Global == nil {
		return nil, errors.New("wire: global database is required")
	}
	secrets := opt.Secrets
	if secrets == nil {
		secrets = secret.NewOS()
	}
	c := opt.Clock
	if c == nil {
		c = clock.System{}
	}
	factory := protocol.NewFactory(opt.HTTPClient)

	roleService, err := role.New(opt.Global, c)
	if err != nil {
		return nil, err
	}
	providerService, err := provider.New(opt.Global, provider.Options{
		Clock:    c,
		Secrets:  secrets,
		Lister:   factory,
		RoleCaps: role.RequiredCapability,
	})
	if err != nil {
		return nil, err
	}
	modelService, err := model.New(opt.Global, model.Options{
		Clock:   c,
		Secrets: secrets,
		Lister:  factory,
	})
	if err != nil {
		return nil, err
	}
	gw, err := gateway.New(gateway.Options{
		Clock:    c,
		Secrets:  secrets,
		Roles:    roleService,
		Factory:  factory,
		Settings: opt.WorkspaceSettings,
		Logger:   opt.Logger,
	})
	if err != nil {
		return nil, err
	}
	return &Stack{
		Providers: providerService,
		Models:    modelService,
		Roles:     roleService,
		Gateway:   gw,
		Chat:      gateway.NewChatService(gw),
		Embedding: gw,
		Rerank:    gw,
	}, nil
}
