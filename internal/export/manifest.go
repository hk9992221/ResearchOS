package export

import (
	"encoding/json"
	"sort"
	"time"

	"researchos/internal/foundation/projects"
)

// manifestVersion is the bundle contract version. Fields are only ever added
// (§4.5); readers must keep accepting older files, hence the string.
const manifestVersion = "1"

// appVersion is the manifest's application version. The application has no
// build-stamped version yet, so the SPEC-00 baseline is reported.
const appVersion = "0.1.0"

// manifest is the JSON skeleton of §4.5. files excludes manifest.json itself.
type manifest struct {
	ManifestVersion string            `json:"manifest_version"`
	App             manifestApp       `json:"app"`
	ExportedAt      string            `json:"exported_at"`
	Format          string            `json:"format"`
	Project         manifestProject   `json:"project"`
	Counts          manifestCounts    `json:"counts"`
	Checksum        manifestHash      `json:"checksum"`
	Files           []manifestFile    `json:"files"`
	Papers          []manifestPaper   `json:"papers"`
	Missing         []manifestMissing `json:"missing"`
	Warnings        []manifestWarning `json:"warnings"`
}

type manifestApp struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	SchemaGlobal  int    `json:"schema_global"`
	SchemaProject int    `json:"schema_project"`
}

type manifestProject struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type manifestCounts struct {
	Notes       int   `json:"notes"`
	Papers      int   `json:"papers"`
	Attachments int   `json:"attachments"`
	Files       int   `json:"files"`
	Bytes       int64 `json:"bytes"`
}

type manifestHash struct {
	Algorithm string `json:"algorithm"`
}

type manifestFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type manifestPaper struct {
	ID        string `json:"id"`
	ZoteroKey string `json:"zotero_key"`
	SourcePDF string `json:"source_pdf"`
	FileHash  string `json:"file_hash"`
	Missing   bool   `json:"missing"`
}

type manifestMissing struct {
	Kind         string `json:"kind"`
	Ref          string `json:"ref"`
	RecordedPath string `json:"recorded_path"`
}

type manifestWarning struct {
	Code string `json:"code"`
	Path string `json:"path"`
}

// projectMeta is meta/project.json: the project registration row.
type projectMeta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	Goals     string `json:"goals"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// paperMeta is one meta/papers.json element: the global row projection plus the
// bundle location of the copy.
type paperMeta struct {
	ID        string          `json:"id"`
	ZoteroKey string          `json:"zotero_key"`
	FileHash  string          `json:"file_hash"`
	FileSize  int64           `json:"file_size"`
	SourcePDF string          `json:"source_pdf"`
	Missing   bool            `json:"missing"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// buildManifest assembles the manifest from what was actually written.
func buildManifest(st *planState, format Format, schemaGlobal, schemaProject int,
	exportedAt time.Time, files []manifestFile, counts manifestCounts,
	missing []MissingRef, warnings []Warning) *manifest {

	sortedFiles := make([]manifestFile, len(files))
	copy(sortedFiles, files)
	sort.Slice(sortedFiles, func(i, j int) bool { return sortedFiles[i].Path < sortedFiles[j].Path })

	m := &manifest{
		ManifestVersion: manifestVersion,
		App: manifestApp{
			Name:          "ResearchOS",
			Version:       appVersion,
			SchemaGlobal:  schemaGlobal,
			SchemaProject: schemaProject,
		},
		ExportedAt: exportedAt.In(time.Local).Format(time.RFC3339),
		Format:     string(format),
		Project: manifestProject{
			ID:     st.project.ID,
			Name:   st.project.Name,
			Status: string(st.project.Status),
		},
		Counts:   counts,
		Checksum: manifestHash{Algorithm: "sha256"},
		Files:    sortedFiles,
		Papers:   make([]manifestPaper, 0, len(st.papers)),
		Missing:  make([]manifestMissing, 0, len(missing)),
		Warnings: make([]manifestWarning, 0, len(warnings)),
	}
	for _, p := range st.papers {
		m.Papers = append(m.Papers, manifestPaper{
			ID:        p.ID,
			ZoteroKey: p.ZoteroKey,
			SourcePDF: p.BundlePath,
			FileHash:  p.FileHash,
			Missing:   p.Missing,
		})
	}
	for _, ref := range missing {
		m.Missing = append(m.Missing, manifestMissing{
			Kind: ref.Kind, Ref: ref.Ref, RecordedPath: ref.RecordedPath,
		})
	}
	for _, w := range warnings {
		m.Warnings = append(m.Warnings, manifestWarning{Code: w.Code, Path: w.Path})
	}
	return m
}

// marshalIndent encodes v as pretty JSON with a trailing newline.
func marshalIndent(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// metaProjectJSON renders meta/project.json.
func metaProjectJSON(p projects.Project) ([]byte, error) {
	return marshalIndent(projectMeta{
		ID:        p.ID,
		Name:      p.Name,
		Summary:   p.Summary,
		Goals:     p.Goals,
		Status:    string(p.Status),
		CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// metaPapersJSON renders meta/papers.json.
func metaPapersJSON(papers []plannedPaper) ([]byte, error) {
	out := make([]paperMeta, 0, len(papers))
	for _, p := range papers {
		entry := paperMeta{
			ID:        p.ID,
			ZoteroKey: p.ZoteroKey,
			FileHash:  p.FileHash,
			FileSize:  p.Bytes,
			SourcePDF: p.BundlePath,
			Missing:   p.Missing,
		}
		if len(p.Metadata) > 0 && json.Valid(p.Metadata) {
			entry.Metadata = json.RawMessage(p.Metadata)
		}
		out = append(out, entry)
	}
	return marshalIndent(out)
}
