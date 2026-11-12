// Package clock provides the injectable time source and the single timestamp
// format used by every database column (SPEC-01 §4.1.2, §5.2).
package clock

import "time"

// Layout is the one and only storage timestamp layout: UTC, millisecond
// precision, fixed width. Its lexicographic order equals chronological order.
const Layout = "2006-01-02T15:04:05.000Z"

// Clock is the minimal injectable time source.
type Clock interface{ Now() time.Time }

// System is the production clock: wall time in UTC.
type System struct{}

// Now implements Clock.
func (System) Now() time.Time { return time.Now().UTC() }

// Fixed is a test clock. Advance moves it forward; it is safe to share the
// pointer between tests and the code under test.
type Fixed struct {
	T time.Time
}

// Now implements Clock.
func (f Fixed) Now() time.Time { return f.T }

// Advance moves the fixed time forward by d.
func (f *Fixed) Advance(d time.Duration) { f.T = f.T.Add(d) }

// Format serializes t in UTC with millisecond precision.
func Format(t time.Time) string { return t.UTC().Format(Layout) }

// Parse reads a timestamp produced by Format. It is strict about the layout so
// that hand-edited files with second or nanosecond precision are rejected.
func Parse(s string) (time.Time, error) { return time.Parse(Layout, s) }
