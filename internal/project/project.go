package project

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/schema"
	"researchos/internal/ports"
)

// Status is the project lifecycle state (§5.3).
type Status string

const (
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

// Field limits of §5.3. They are rune counts of the stored text.
const (
	maxNameRunes    = 200
	maxSummaryRunes = 5000
	maxGoalsRunes   = 100000
	maxFocusRunes   = 20000
)

// Project is the public projection of a global.db.projects row (§5.3). Status
// is passed through verbatim, so a row with an unknown status is reported as-is
// for reconcile to flag rather than silently coerced.
type Project struct {
	ID         string
	Name       string
	Summary    string
	Goals      string
	FocusMD    string
	Status     Status
	CreatedAt  time.Time
	ArchivedAt *time.Time
}

// CreateInput creates a project. Name is trimmed before validation and storage;
// duplicates are allowed (§5.3).
type CreateInput struct {
	Name    string
	Summary string
	Goals   string
}

// UpdateInput edits a project. A nil pointer leaves the field untouched.
//
// Actor is the additive field that keeps the spec signature
// Update(ctx, UpdateInput): §7.1 requires the caller's actor on the emitted
// entity.updated event. An empty Actor means the in-app user; only user and mcp
// are accepted.
type UpdateInput struct {
	ID      string
	Name    *string
	Summary *string
	Goals   *string
	Focus   *string
	Actor   ports.Actor
}

// ListQuery filters List. Query is a case-insensitive substring of name or
// summary.
type ListQuery struct {
	IncludeArchived bool
	Query           string
}

// Stats is the per-project aggregate of §5.3. LastActivityAt is nil when the
// project has no activities.
type Stats struct {
	NoteCount       int
	IdeaActive      int
	ExperimentCount int
	TaskOpen        int
	TaskBlocked     int
	PaperCount      int
	TrashedCount    int
	DirBytes        int64
	LastActivityAt  *time.Time
}

// Session is the single active project session. Writable is false for archived
// projects.
type Session struct {
	Project  Project
	Writable bool
}

// toProject converts a store row.
func toProject(p projects.Project) Project {
	return Project{
		ID:         p.ID,
		Name:       p.Name,
		Summary:    p.Summary,
		Goals:      p.Goals,
		FocusMD:    p.FocusMD,
		Status:     Status(p.Status),
		CreatedAt:  p.CreatedAt,
		ArchivedAt: p.ArchivedAt,
	}
}

// normalizeName trims and validates a project name (§5.3): 1..200 runes after
// trimming and no control characters \x00-\x1f. Duplicates are legal.
func normalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", &InvalidFieldError{Field: "name", Reason: "must not be empty", Err: ErrNameInvalid}
	}
	if n := utf8.RuneCountInString(name); n > maxNameRunes {
		return "", &InvalidFieldError{
			Field:  "name",
			Reason: fmt.Sprintf("must be at most %d runes, got %d", maxNameRunes, n),
			Err:    ErrNameInvalid,
		}
	}
	for _, r := range name {
		if r < 0x20 {
			return "", &InvalidFieldError{
				Field:  "name",
				Reason: fmt.Sprintf("must not contain control characters (found %q)", r),
				Err:    ErrNameInvalid,
			}
		}
	}
	return name, nil
}

// validateText enforces a rune limit on a free-text field.
func validateText(field, value string, max int) error {
	if n := utf8.RuneCountInString(value); n > max {
		return &InvalidFieldError{
			Field:  field,
			Reason: fmt.Sprintf("must be at most %d runes, got %d", max, n),
			Err:    ErrInvalidInput,
		}
	}
	return nil
}

// updateActor resolves the actor of an edit: an omitted actor means the in-app
// user, anything other than user/mcp is rejected (§7.1).
func updateActor(a ports.Actor) (ports.Actor, error) {
	switch a {
	case "":
		return ports.ActorUser, nil
	case ports.ActorUser, ports.ActorMCP:
		return a, nil
	default:
		return "", &InvalidActorError{Actor: a}
	}
}

// statusActor validates the actor of a state change: only user and mcp may
// archive or unarchive (§5.3, PRJ-03).
func statusActor(a ports.Actor) (ports.Actor, error) {
	switch a {
	case ports.ActorUser, ports.ActorMCP:
		return a, nil
	default:
		return "", &InvalidActorError{Actor: a}
	}
}

// schemaStatus converts the public status to the store enum.
func schemaStatus(s Status) schema.ProjectStatus { return schema.ProjectStatus(s) }
