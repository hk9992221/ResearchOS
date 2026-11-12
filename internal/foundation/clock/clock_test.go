package clock

import (
	"testing"
	"time"
)

func TestFormatParseRoundTrip(t *testing.T) {
	instant := time.Date(2026, 11, 12, 13, 45, 1, 123_000_000, time.UTC)
	got := Format(instant)
	if want := "2026-11-12T13:45:01.123Z"; got != want {
		t.Fatalf("Format = %q, want %q", got, want)
	}
	back, err := Parse(got)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !back.Equal(instant) {
		t.Fatalf("Parse round trip = %v, want %v", back, instant)
	}
	if back.Location() != time.UTC {
		t.Fatalf("Parse location = %v, want UTC", back.Location())
	}
}

func TestFormatConvertsToUTC(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	instant := time.Date(2026, 11, 12, 21, 45, 1, 500_000_000, loc)
	if got, want := Format(instant), "2026-11-12T13:45:01.500Z"; got != want {
		t.Fatalf("Format = %q, want %q", got, want)
	}
}

func TestFormatTruncatesSubMillisecond(t *testing.T) {
	instant := time.Date(2026, 1, 2, 3, 4, 5, 999_999, time.UTC)
	if got, want := Format(instant), "2026-01-02T03:04:05.000Z"; got != want {
		t.Fatalf("Format = %q, want %q", got, want)
	}
}

func TestParseRejectsNonCanonicalForms(t *testing.T) {
	for _, s := range []string{
		"",
		"2026-11-12T13:45:01Z",
		"2026-11-12T13:45:01.1Z",
		"2026-11-12 13:45:01.123Z",
		"2026-11-12T13:45:01.123+08:00",
		"not-a-time",
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", s)
		}
	}
}

func TestFixedAdvance(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := &Fixed{T: start}
	if !f.Now().Equal(start) {
		t.Fatalf("Now = %v, want %v", f.Now(), start)
	}
	f.Advance(90 * time.Second)
	want := start.Add(90 * time.Second)
	if !f.Now().Equal(want) {
		t.Fatalf("Now after advance = %v, want %v", f.Now(), want)
	}
}

func TestSystemReturnsUTC(t *testing.T) {
	now := System{}.Now()
	if now.Location() != time.UTC {
		t.Fatalf("System.Now location = %v, want UTC", now.Location())
	}
}
