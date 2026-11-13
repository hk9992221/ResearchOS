package adapters

import (
	"context"
	"database/sql"
	"fmt"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/trash"
	"researchos/internal/foundation/workspace"
	"researchos/internal/ports"
)

// Trash adapts the foundation recycle bin to ports.TrashService. The trash
// service is bound to one project database, so it is rebuilt per call from the
// current workspace handle; that is cheap and stays correct after the handle is
// closed and reopened.
type Trash struct {
	ws      *workspace.Workspace
	confirm confirm.Service
	clock   clock.Clock
}

var _ ports.TrashService = (*Trash)(nil)

// NewTrash binds the adapter to the workspace and the project-independent
// collaborators of trash.New. A nil clock means clock.System.
func NewTrash(ws *workspace.Workspace, conf confirm.Service, c clock.Clock) *Trash {
	if c == nil {
		c = clock.System{}
	}
	return &Trash{ws: ws, confirm: conf, clock: c}
}

// List returns one project's recycle bin, newest deletion first.
func (t *Trash) List(ctx context.Context, projectID string) ([]ports.TrashItem, error) {
	svc, _, err := t.service(ctx, projectID)
	if err != nil {
		return nil, err
	}
	items, err := svc.List(ctx, trash.ListQuery{})
	if err != nil {
		return nil, err
	}
	out := make([]ports.TrashItem, len(items))
	for i, item := range items {
		out[i] = ports.TrashItem{
			EntityType: string(item.EntityType),
			EntityID:   item.ID,
			Title:      item.Title,
			DeletedAt:  item.DeletedAt,
		}
	}
	return out, nil
}

// Restore clears an entity's deleted_at inside one transaction. An unknown or
// non-soft-deletable type surfaces the foundation error unchanged; this adapter
// does not duplicate the foundation's validation.
func (t *Trash) Restore(ctx context.Context, projectID, entityType, entityID string, actor ports.Actor) error {
	svc, handle, err := t.service(ctx, projectID)
	if err != nil {
		return err
	}
	return handle.InTx(ctx, func(tx *sql.Tx) error {
		return svc.Restore(ctx, tx, trash.Ref{
			Type: schema.EntityType(entityType),
			ID:   entityID,
		}, actorOrUser(actor))
	})
}

// Purge permanently deletes the requested entities. Confirmation errors are
// returned unchanged so callers can match errs.ErrUserConfirmationRequired and
// errs.ErrConfirmScopeMismatch with errors.Is; the token is consumed by the
// foundation service exactly once.
func (t *Trash) Purge(ctx context.Context, projectID string, items []ports.TrashRef, token confirm.Token) error {
	svc, _, err := t.service(ctx, projectID)
	if err != nil {
		return err
	}
	refs := make([]trash.Ref, len(items))
	for i, item := range items {
		refs[i] = trash.Ref{
			Type: schema.EntityType(item.EntityType),
			ID:   item.EntityID,
		}
	}
	// The port carries no actor: purges are always initiated in-app by the
	// user, so the purge events are attributed to the local user.
	_, err = svc.Purge(ctx, trash.PurgeRequest{Refs: refs, Confirm: token, Actor: schema.ActorUser})
	return err
}

// service resolves the project handle and builds the project-scoped trash
// service. Purge and Restore also need the handle; callers that do not use it
// discard the second result.
func (t *Trash) service(ctx context.Context, projectID string) (*trash.Service, *workspace.ProjectHandle, error) {
	if t == nil || t.ws == nil {
		return nil, nil, fmt.Errorf("adapters: trash: %w", ErrUnavailable)
	}
	handle, err := t.ws.Project(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	svc := trash.New(projectID, handle.DB(), t.ws.Paths(), handle.Events(), t.confirm, t.clock)
	return svc, handle, nil
}
