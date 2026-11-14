package boot

// The AI settings tab reaches the SPEC-02 service stack through the narrow
// pages.AIAPI surface. These methods are thin pass-throughs: the services own
// validation, key handling and role resolution, while the page only renders
// the key-free views they return.

import (
	"context"

	"researchos/internal/ai/model"
	"researchos/internal/ai/provider"
	"researchos/internal/ai/role"
)

// Available reports whether the workspace has a usable AI stack.
func (m *aiModule) Available() bool { return m.available() }

// ChatReady reports the local chat_main assignment probe the modules use.
func (m *aiModule) ChatReady() bool { return m.chatReady() }

func (m *aiModule) ListProviders(ctx context.Context) ([]provider.ProviderView, error) {
	if !m.available() {
		return nil, errAIUnavailable
	}
	return m.stack.Providers.List(ctx)
}

func (m *aiModule) CreateProvider(ctx context.Context, in provider.CreateProviderInput) (provider.ProviderView, error) {
	if !m.available() {
		return provider.ProviderView{}, errAIUnavailable
	}
	view, err := m.stack.Providers.Create(ctx, in)
	if err == nil {
		m.RefreshReady()
	}
	return view, err
}

func (m *aiModule) UpdateProvider(ctx context.Context, id string, in provider.UpdateProviderInput) (provider.ProviderView, error) {
	if !m.available() {
		return provider.ProviderView{}, errAIUnavailable
	}
	view, err := m.stack.Providers.Update(ctx, id, in)
	if err == nil {
		m.RefreshReady()
	}
	return view, err
}

func (m *aiModule) DeleteProvider(ctx context.Context, id string) error {
	if !m.available() {
		return errAIUnavailable
	}
	err := m.stack.Providers.Delete(ctx, id)
	if err == nil {
		m.RefreshReady()
	}
	return err
}

func (m *aiModule) TestProvider(ctx context.Context, id string) (provider.TestResult, error) {
	if !m.available() {
		return provider.TestResult{}, errAIUnavailable
	}
	return m.stack.Providers.Test(ctx, id)
}

func (m *aiModule) ListModels(ctx context.Context, q model.ModelQuery) ([]model.ModelInfo, error) {
	if !m.available() {
		return nil, errAIUnavailable
	}
	return m.stack.Models.List(ctx, q)
}

func (m *aiModule) SyncModels(ctx context.Context, providerID string) (model.ModelSyncResult, error) {
	if !m.available() {
		return model.ModelSyncResult{}, errAIUnavailable
	}
	result, err := m.stack.Models.Sync(ctx, providerID)
	if err == nil {
		m.RefreshReady()
	}
	return result, err
}

func (m *aiModule) AddManualModel(ctx context.Context, providerID, modelID, displayName string) error {
	if !m.available() {
		return errAIUnavailable
	}
	err := m.stack.Models.AddManual(ctx, providerID, modelID, displayName)
	if err == nil {
		m.RefreshReady()
	}
	return err
}

func (m *aiModule) RemoveManualModel(ctx context.Context, providerID, modelID string) error {
	if !m.available() {
		return errAIUnavailable
	}
	err := m.stack.Models.RemoveManual(ctx, providerID, modelID)
	if err == nil {
		m.RefreshReady()
	}
	return err
}

func (m *aiModule) ListRoles(ctx context.Context) ([]role.RoleView, error) {
	if !m.available() {
		return nil, errAIUnavailable
	}
	return m.stack.Roles.List(ctx)
}

func (m *aiModule) AssignRole(ctx context.Context, r role.Role, providerID, modelID string) (role.AssignResult, error) {
	if !m.available() {
		return role.AssignResult{}, errAIUnavailable
	}
	result, err := m.stack.Roles.Assign(ctx, r, providerID, modelID)
	if err == nil {
		m.RefreshReady()
	}
	return result, err
}

func (m *aiModule) ClearRole(ctx context.Context, r role.Role) error {
	if !m.available() {
		return errAIUnavailable
	}
	err := m.stack.Roles.Clear(ctx, r)
	if err == nil {
		m.RefreshReady()
	}
	return err
}
