package kanban

import (
	"testing"
	"time"

	"researchos/internal/foundation/schema"
)

// TestBoardScaleProbe is the K-11 acceptance probe: one column with 1000
// live top-level tasks must render through a single aggregate query. The
// spec target is ≤200ms on local SSD; the value is recorded each run.
func TestBoardScaleProbe(t *testing.T) {
	e := newTestEnv(t)
	start := time.Now()
	for i := range 1000 {
		if _, err := e.m.Tasks().Create(e.ctx, schema.ActorUser,
			CreateInput{Title: "批量任务", Column: "Todo"}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	seed := time.Since(start)

	start = time.Now()
	view, err := e.m.Views().Board(e.ctx, BoardQuery{})
	took := time.Since(start)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	for _, c := range view.Columns {
		if c.Column.Name == "Todo" && len(c.Tasks) != 1000 {
			t.Fatalf("todo cards = %d", len(c.Tasks))
		}
	}
	t.Logf("K-11 numbers: seed(1000)=%v board=%d cards in %v", seed, 1000, took)
	// Generous CI margin; the recorded value above is the acceptance number.
	if took > 2*time.Second {
		t.Fatalf("board of 1000 tasks took %v", took)
	}
}
