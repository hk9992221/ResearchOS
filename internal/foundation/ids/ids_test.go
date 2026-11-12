package ids

import (
	"testing"
	"time"

	"researchos/internal/foundation/clock"
)

// steppingClock advances one millisecond every 100 calls so that the timestamp
// prefix is exercised without depending on wall-clock behaviour.
type steppingClock struct {
	t time.Time
	n int
}

func (c *steppingClock) Now() time.Time {
	c.n++
	if c.n%100 == 0 {
		c.t = c.t.Add(time.Millisecond)
	}
	return c.t
}

func TestAC04TenThousandIDs(t *testing.T) {
	c := &steppingClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	seen := make(map[string]struct{}, 10_000)
	var prevPrefix string
	for i := range 10_000 {
		id, err := New(c)
		if err != nil {
			t.Fatalf("New #%d: %v", i, err)
		}
		if !Valid(id) {
			t.Fatalf("New #%d returned invalid id %q", i, id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q at #%d", id, i)
		}
		seen[id] = struct{}{}
		prefix := TimestampPrefix(id)
		if i > 0 && prefix < prevPrefix {
			t.Fatalf("timestamp prefix went backwards: %q after %q at #%d", prefix, prevPrefix, i)
		}
		prevPrefix = prefix
	}
}

func TestAC04FixedClockUnique(t *testing.T) {
	c := &clock.Fixed{T: time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)}
	seen := make(map[string]struct{}, 1_000)
	for i := range 1_000 {
		id, err := New(c)
		if err != nil {
			t.Fatalf("New #%d: %v", i, err)
		}
		if !Valid(id) {
			t.Fatalf("invalid id %q", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id under fixed clock: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewNilClock(t *testing.T) {
	id, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	if !Valid(id) {
		t.Fatalf("invalid id %q", id)
	}
}

func TestMustNew(t *testing.T) {
	id := MustNew(&clock.Fixed{T: time.Unix(0, 0).UTC()})
	if !Valid(id) {
		t.Fatalf("MustNew returned invalid id %q", id)
	}
}

func TestValidRejectsMalformed(t *testing.T) {
	valid := "0192f0c8-1234-7000-8000-000000000000"
	if !Valid(valid) {
		t.Fatalf("Valid(%q) = false, want true", valid)
	}
	for _, s := range []string{
		"",
		"0192f0c8123470008000000000000000",      // no hyphens
		"0192F0C8-1234-7000-8000-000000000000",  // uppercase
		"0192f0c8-1234-4000-8000-000000000000",  // version 4
		"0192f0c8-1234-7000-0000-000000000000",  // variant 0
		"0192f0c8-1234-7000-c000-000000000000",  // variant 11
		"0192f0c8-1234-7000-8000-00000000000g",  // non-hex
		"0192f0c8_1234_7000_8000_000000000000",  // wrong separators
		"0192f0c8-1234-7000-8000-0000000000000", // too long
		" 192f0c8-1234-7000-8000-000000000000",  // leading space
	} {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}

func TestTimestampPrefix(t *testing.T) {
	// 0x0192f0c81234 milliseconds
	id := "0192f0c8-1234-7000-8000-000000000000"
	if got, want := TimestampPrefix(id), "0192f0c8-1234"; got != want {
		t.Fatalf("TimestampPrefix = %q, want %q", got, want)
	}
	if got := TimestampPrefix("abc"); got != "abc" {
		t.Fatalf("TimestampPrefix short = %q", got)
	}
}
