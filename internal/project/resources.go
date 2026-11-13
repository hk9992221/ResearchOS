package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/ports"
)

// Resources is the Resources page projection (§5.5, PRJ-05).
type Resources struct {
	Papers      []ports.PaperBrief
	NoteCounts  map[string]int
	Notes       []ports.NoteBrief
	Experiments []ports.ExperimentBrief
	TaskBuckets map[string]int
	Tasks       []ports.TaskBrief
}

// Resources loads the four resource sections. Archived projects are readable.
func (s *Service) Resources(ctx context.Context, projectID string) (*Resources, error) {
	if _, err := s.Get(ctx, projectID); err != nil {
		return nil, err
	}
	if s.ws == nil {
		return nil, errNoWorkspace
	}
	h, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project: resources %s: %w", projectID, err)
	}
	db := h.DB()
	out := &Resources{
		Papers:      []ports.PaperBrief{},
		NoteCounts:  make(map[string]int),
		Notes:       []ports.NoteBrief{},
		Experiments: []ports.ExperimentBrief{},
		TaskBuckets: make(map[string]int),
		Tasks:       []ports.TaskBrief{},
	}

	if s.papers != nil {
		papers, perr := s.papers.ListProjectPapers(ctx, projectID, 20)
		if perr == nil {
			out.Papers = papers
		} else {
			if out.Papers, err = s.paperBriefsFromDB(ctx, projectID, db); err != nil {
				return nil, err
			}
		}
	} else {
		if out.Papers, err = s.paperBriefsFromDB(ctx, projectID, db); err != nil {
			return nil, err
		}
	}

	if out.NoteCounts, err = s.noteCounts(ctx, db); err != nil {
		return nil, err
	}
	if out.Notes, err = s.noteBriefs(ctx, db); err != nil {
		return nil, err
	}
	if out.Experiments, err = s.experimentBriefs(ctx, db); err != nil {
		return nil, err
	}
	if out.TaskBuckets, err = s.taskBuckets(ctx, db); err != nil {
		return nil, err
	}
	if out.Tasks, err = s.taskBriefs(ctx, db); err != nil {
		return nil, err
	}
	return out, nil
}

// paperBriefsFromDB is the PaperIndex fallback: project_papers plus the global
// papers row.
func (s *Service) paperBriefsFromDB(ctx context.Context, projectID string, db *sql.DB) ([]ports.PaperBrief, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT paper_id, linked_at FROM project_papers
		 ORDER BY linked_at DESC, paper_id LIMIT 20`)
	if err != nil {
		return nil, fmt.Errorf("project: resources %s: papers: %w", projectID, err)
	}
	type link struct {
		id       string
		linkedAt time.Time
	}
	var links []link
	for rows.Next() {
		var (
			id  string
			raw string
		)
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		at, err := clock.Parse(raw)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("project: resources %s: paper linked_at: %w", projectID, err)
		}
		links = append(links, link{id: id, linkedAt: at})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(links) == 0 {
		return []ports.PaperBrief{}, nil
	}

	global, err := s.globalHandle()
	if err != nil {
		return nil, err
	}
	args := make([]any, len(links))
	for i, l := range links {
		args[i] = l.id
	}
	prows, err := global.DB().QueryContext(ctx,
		`SELECT id, metadata, file_state FROM papers WHERE id IN (`+placeholders(len(args))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("project: resources %s: global papers: %w", projectID, err)
	}
	defer prows.Close()
	type paperRow struct {
		title   string
		authors string
		year    int
		missing bool
	}
	byID := make(map[string]paperRow, len(links))
	for prows.Next() {
		var (
			id, rawMeta, state string
		)
		if err := prows.Scan(&id, &rawMeta, &state); err != nil {
			return nil, err
		}
		title, authors, year := parsePaperMetadata(rawMeta)
		byID[id] = paperRow{title: title, authors: authors, year: year, missing: state != "ok"}
	}
	if err := prows.Err(); err != nil {
		return nil, err
	}
	out := make([]ports.PaperBrief, 0, len(links))
	for _, l := range links {
		row, ok := byID[l.id]
		out = append(out, ports.PaperBrief{
			ID:       l.id,
			Title:    row.title,
			Authors:  row.authors,
			Year:     row.year,
			LinkedAt: l.linkedAt,
			Missing:  !ok || row.missing,
		})
	}
	return out, nil
}

// parsePaperMetadata extracts the display fields from a Zotero metadata blob.
func parsePaperMetadata(raw string) (title, authors string, year int) {
	var meta struct {
		Title    string `json:"title"`
		Date     string `json:"date"`
		Creators []struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
			Name      string `json:"name"`
		} `json:"creators"`
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return "", "", 0
	}
	names := make([]string, 0, len(meta.Creators))
	for _, c := range meta.Creators {
		switch {
		case c.Name != "":
			names = append(names, c.Name)
		case c.LastName != "" && c.FirstName != "":
			names = append(names, c.LastName+", "+c.FirstName)
		case c.LastName != "":
			names = append(names, c.LastName)
		case c.FirstName != "":
			names = append(names, c.FirstName)
		}
	}
	return meta.Title, strings.Join(names, ", "), yearFromDate(meta.Date)
}

// yearFromDate finds the first four-digit year in a Zotero date string.
func yearFromDate(date string) int {
	for i := 0; i+4 <= len(date); i++ {
		chunk := date[i : i+4]
		if _, err := strconv.Atoi(chunk); err != nil {
			continue
		}
		prevOK := i == 0 || date[i-1] < '0' || date[i-1] > '9'
		next := i + 4
		nextOK := next == len(date) || date[next] < '0' || date[next] > '9'
		if prevOK && nextOK {
			n, _ := strconv.Atoi(chunk)
			return n
		}
	}
	return 0
}

func (s *Service) noteCounts(ctx context.Context, db *sql.DB) (map[string]int, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT kind, count(*) FROM notes WHERE deleted_at IS NULL GROUP BY kind ORDER BY kind`)
	if err != nil {
		return nil, fmt.Errorf("project: resources: note counts: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var (
			kind string
			n    int
		)
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) noteBriefs(ctx context.Context, db *sql.DB) ([]ports.NoteBrief, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, title, kind, updated_at FROM notes
		 WHERE deleted_at IS NULL ORDER BY updated_at DESC, id DESC LIMIT 20`)
	if err != nil {
		return nil, fmt.Errorf("project: resources: notes: %w", err)
	}
	defer rows.Close()
	out := make([]ports.NoteBrief, 0, 20)
	for rows.Next() {
		var (
			id, title, kind, raw string
		)
		if err := rows.Scan(&id, &title, &kind, &raw); err != nil {
			return nil, err
		}
		at, err := clock.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("project: resources: note updated_at: %w", err)
		}
		out = append(out, ports.NoteBrief{ID: id, Title: title, Kind: kind, UpdatedAt: at})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) experimentBriefs(ctx context.Context, db *sql.DB) ([]ports.ExperimentBrief, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT e.id, e.title, e.updated_at,
		        (SELECT r.status FROM runs r
		          WHERE r.experiment_id = e.id AND r.deleted_at IS NULL
		          ORDER BY r.created_at DESC, r.id DESC LIMIT 1)
		   FROM experiments e
		  WHERE e.deleted_at IS NULL
		  ORDER BY e.updated_at DESC, e.id DESC
		  LIMIT 10`)
	if err != nil {
		return nil, fmt.Errorf("project: resources: experiments: %w", err)
	}
	defer rows.Close()
	out := make([]ports.ExperimentBrief, 0, 10)
	for rows.Next() {
		var (
			id, title, raw string
			status         sql.NullString
		)
		if err := rows.Scan(&id, &title, &raw, &status); err != nil {
			return nil, err
		}
		at, err := clock.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("project: resources: experiment updated_at: %w", err)
		}
		out = append(out, ports.ExperimentBrief{
			ID:            id,
			Title:         title,
			LastRunStatus: status.String,
			UpdatedAt:     at,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) taskBuckets(ctx context.Context, db *sql.DB) (map[string]int, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT "column", count(*) FROM tasks WHERE deleted_at IS NULL GROUP BY "column" ORDER BY "column"`)
	if err != nil {
		return nil, fmt.Errorf("project: resources: task buckets: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var (
			column string
			n      int
		)
		if err := rows.Scan(&column, &n); err != nil {
			return nil, err
		}
		out[column] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) taskBriefs(ctx context.Context, db *sql.DB) ([]ports.TaskBrief, error) {
	scope, err := taskTerminalScope(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, title, "column", priority, deadline, blocked, updated_at
		   FROM tasks
		  WHERE deleted_at IS NULL`+scope+`
		  ORDER BY (deadline IS NULL), deadline,
		           CASE lower(coalesce(priority, ''))
		             WHEN 'critical' THEN 4
		             WHEN 'urgent' THEN 4
		             WHEN 'high' THEN 3
		             WHEN 'medium' THEN 2
		             WHEN 'low' THEN 1
		             ELSE CASE WHEN priority GLOB '[0-9]*' THEN CAST(priority AS INTEGER) ELSE 0 END
		           END DESC,
		           updated_at DESC, id DESC
		  LIMIT 50`)
	if err != nil {
		return nil, fmt.Errorf("project: resources: tasks: %w", err)
	}
	defer rows.Close()
	out := make([]ports.TaskBrief, 0, 50)
	for rows.Next() {
		var (
			id, title, column, raw string
			priorityRaw            sql.NullString
			deadlineRaw            sql.NullString
			blocked                int
		)
		if err := rows.Scan(&id, &title, &column, &priorityRaw, &deadlineRaw, &blocked, &raw); err != nil {
			return nil, err
		}
		at, err := clock.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("project: resources: task updated_at: %w", err)
		}
		task := ports.TaskBrief{
			ID:        id,
			Title:     title,
			Column:    column,
			Priority:  taskPriority(priorityRaw.String),
			Blocked:   blocked == 1,
			UpdatedAt: at,
		}
		if deadlineRaw.Valid && deadlineRaw.String != "" {
			deadline, err := clock.Parse(deadlineRaw.String)
			if err != nil {
				return nil, fmt.Errorf("project: resources: task deadline: %w", err)
			}
			task.Deadline = &deadline
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// taskPriority maps the SPEC-08 text priority to the integer projection.
func taskPriority(raw string) int {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical", "urgent":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
		return n
	}
	return 0
}
