package workspace

import (
	"context"
	"database/sql"
	"reflect"
	"sync"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
)

// txState is one open transaction together with the database it belongs to.
type txState struct {
	db *sql.DB
	tx *sql.Tx
}

// txCarrier implements the single transaction entry point. Transactions are
// registered per context identity, so a nested InTx call made with the same
// context reuses the outer transaction instead of opening a second one.
//
// Concurrent top-level operations must pass distinct context values (the normal
// Go convention: derive a context per operation). Sharing one context value
// across goroutines is indistinguishable from a nested call with the fixed
// func(tx *sql.Tx) signature, so it would join the transaction.
type txCarrier struct {
	mu     sync.Mutex
	active map[context.Context]*txState
}

func (c *txCarrier) inTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !comparableContext(ctx) {
		// A non-comparable custom context cannot key the registry; run without
		// nested reuse rather than panic.
		return runIsolatedTx(ctx, db, fn)
	}
	c.mu.Lock()
	if c.active != nil {
		if st, ok := c.active[ctx]; ok {
			c.mu.Unlock()
			return fn(st.tx)
		}
	}
	c.mu.Unlock()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.active == nil {
		c.active = make(map[context.Context]*txState)
	}
	if st, ok := c.active[ctx]; ok {
		// A concurrent call with the same context won the race; use its tx.
		c.mu.Unlock()
		_ = tx.Rollback()
		return fn(st.tx)
	}
	c.active[ctx] = &txState{db: db, tx: tx}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.active, ctx)
		c.mu.Unlock()
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func runIsolatedTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func comparableContext(ctx context.Context) bool {
	t := reflect.TypeOf(ctx)
	return t != nil && t.Comparable()
}

// GlobalHandle is the handle of the workspace global database.
type GlobalHandle struct {
	db *sql.DB
	tx txCarrier
}

func newGlobalHandle(db *sql.DB) *GlobalHandle { return &GlobalHandle{db: db} }

// DB returns the underlying connection pool.
func (g *GlobalHandle) DB() *sql.DB { return g.db }

// InTx runs fn inside a transaction; an error rolls back. Nested calls with the
// same context reuse the outer transaction.
func (g *GlobalHandle) InTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return g.tx.inTx(ctx, g.db, fn)
}

// Close closes the connection pool.
func (g *GlobalHandle) Close() error { return g.db.Close() }

// ProjectHandle is the handle of one project database.
type ProjectHandle struct {
	// ID is the project UUID.
	ID string

	db     *sql.DB
	tx     txCarrier
	events *events.Service
}

func newProjectHandle(id string, db *sql.DB, c clock.Clock) *ProjectHandle {
	return &ProjectHandle{ID: id, db: db, events: events.New(db, events.Options{Clock: c})}
}

// DB returns the underlying connection pool.
func (h *ProjectHandle) DB() *sql.DB { return h.db }

// Events returns the activity writer bound to this project database.
func (h *ProjectHandle) Events() events.Writer { return h.events }

// Reader returns the activity reader bound to this project database.
func (h *ProjectHandle) Reader() events.Reader { return h.events }

// InTx runs fn inside a transaction; an error rolls back. Nested calls with the
// same context reuse the outer transaction.
func (h *ProjectHandle) InTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return h.tx.inTx(ctx, h.db, fn)
}

// Close closes the connection pool.
func (h *ProjectHandle) Close() error { return h.db.Close() }
