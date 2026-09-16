// Package recoveryinventory produces deterministic, source-only recovery inventories.
package recoveryinventory

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

const schemaVersion = "recovery-inventory/v1"

// Record is a structural source claim. It deliberately contains no source body,
// runtime value, database row, remote URL, or project selector.
type Record struct {
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	Line           int    `json:"line,omitempty"`
	Name           string `json:"name,omitempty"`
	Parser         string `json:"parser,omitempty"`
	Default        string `json:"default,omitempty"`
	Classification string `json:"classification"`
	ClaimOnly      bool   `json:"claim_only,omitempty"`
}

// Report is a deterministic, read-only inventory of current source declarations.
type Report struct {
	SchemaVersion string   `json:"schema_version"`
	Inventory     string   `json:"inventory"`
	SourceOnly    bool     `json:"source_only"`
	Records       []Record `json:"records"`
}

type sourceFile struct {
	absolute string
	relative string
}

func newReport(inventory string) Report {
	return Report{
		SchemaVersion: schemaVersion,
		Inventory:     inventory,
		SourceOnly:    true,
	}
}

func (r *Report) add(record Record) {
	r.Records = append(r.Records, record)
}

func (r *Report) finish() {
	sort.Slice(r.Records, func(i, j int) bool {
		a, b := r.Records[i], r.Records[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Classification < b.Classification
	})

	out := r.Records[:0]
	for _, record := range r.Records {
		if len(out) == 0 || out[len(out)-1] != record {
			out = append(out, record)
		}
	}
	r.Records = out
}

func sourceFiles(root string, extensions ...string) ([]sourceFile, error) {
	allowed := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		allowed[extension] = struct{}{}
	}

	var files []sourceFile
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".agent", ".serena", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".env") {
			return nil
		}
		if len(allowed) > 0 {
			if _, ok := allowed[strings.ToLower(filepath.Ext(entry.Name()))]; !ok {
				return nil
			}
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relative source path: %w", err)
		}
		files = append(files, sourceFile{absolute: path, relative: filepath.ToSlash(relative)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk source root: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].relative < files[j].relative })
	return files, nil
}

func sensitiveEnvironmentName(name string) bool {
	name = "_" + strings.ToUpper(name) + "_"
	return strings.Contains(name, "_SECRET_") ||
		strings.Contains(name, "_PASSWORD_") ||
		strings.Contains(name, "_CREDENTIAL_") ||
		strings.Contains(name, "_TOKEN_") ||
		strings.Contains(name, "_API_KEY_") ||
		strings.Contains(name, "_APIKEY_") ||
		strings.Contains(name, "_PRIVATE_KEY_")
}

func redactedName(name string) string {
	if sensitiveEnvironmentName(name) {
		return "redacted"
	}
	return name
}

// ScanAll returns every AR-1 source inventory in a stable inventory order.
func ScanAll(root string) ([]Report, error) {
	scanners := []func(string) (Report, error){ScanSource, ScanFlags, ScanSurfaces, ScanProjectData}
	reports := make([]Report, 0, len(scanners))
	for _, scan := range scanners {
		report, err := scan(root)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}
