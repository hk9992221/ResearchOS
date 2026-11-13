package projects

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"researchos/internal/foundation/ids"
)

func TestCreateWithID(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	id := ids.MustNew(fixed)
	inTx(t, db, func(tx *sql.Tx) error {
		return store.CreateWithID(ctx, tx, id, "Named", "summary", "goals")
	})
	p, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.ID != id || p.Name != "Named" || p.Summary != "summary" || p.Goals != "goals" {
		t.Fatalf("Get = %+v", p)
	}
	if !p.CreatedAt.Equal(fixed.Now()) {
		t.Fatalf("CreatedAt = %v, want %v", p.CreatedAt, fixed.Now())
	}
}

func TestCreateWithIDRejectsBadID(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	for _, id := range []string{"", "not-a-uuid", ids.MustNew(fixed) + "x"} {
		err := store.CreateWithID(ctx, nil, id, "Name", "", "")
		if err == nil {
			t.Fatalf("CreateWithID(%q) = nil, want error", id)
		}
	}
	if n := countRows(t, db); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestCreateWithIDDuplicate(t *testing.T) {
	store, db := openStore(t)
	ctx := context.Background()
	id := ids.MustNew(fixed)
	inTx(t, db, func(tx *sql.Tx) error {
		return store.CreateWithID(ctx, tx, id, "First", "", "")
	})
	err := store.CreateWithID(ctx, nil, id, "Second", "", "")
	if err == nil {
		t.Fatal("duplicate CreateWithID = nil, want error")
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("duplicate error should be a constraint violation, got %v", err)
	}
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}
