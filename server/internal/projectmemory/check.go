// Package projectmemory is the single server-side definition of the project
// memory checklist.  Consumers (HTTP handlers, the CLI-facing API, and the
// daemon) use this package instead of copying paths or validation rules.
package projectmemory

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	LocationAgents   = "agents"
	LocationContext  = "context"
	LocationADR      = "adr"
	LocationDocs     = "docs_index"
	LocationEvidence = "evidence_index"
)

// Location describes one stable slot in a project's memory contract.
type Location struct {
	Key  string `json:"key"`
	Path string `json:"path"`
	Kind string `json:"kind"` // file or directory
}

var locations = []Location{
	{Key: LocationAgents, Path: "AGENTS.md", Kind: "file"},
	{Key: LocationContext, Path: "CONTEXT.md", Kind: "file"},
	{Key: LocationADR, Path: "docs/adr/", Kind: "directory"},
	{Key: LocationDocs, Path: "docs/README.md", Kind: "file"},
	{Key: LocationEvidence, Path: "docs/evidence/INDEX.md", Kind: "file"},
}

// Locations returns a copy so callers cannot mutate the contract globally.
func Locations() []Location {
	result := make([]Location, len(locations))
	copy(result, locations)
	return result
}

// LocationKeys is the stable key list. Close audits and CLI help read this
// instead of keeping a second copy of the checklist.
func LocationKeys() []string {
	keys := make([]string, len(locations))
	for i, location := range locations {
		keys[i] = location.Key
	}
	return keys
}

// KnownLocation reports whether key is one of the checklist slots.
func KnownLocation(key string) bool {
	for _, location := range locations {
		if location.Key == key {
			return true
		}
	}
	return false
}

// LocationResult is the daemon's read-only observation of one checklist slot.
type LocationResult struct {
	Location
	Exists      bool       `json:"exists"`
	IsDirectory bool       `json:"is_directory"`
	ModifiedAt  *time.Time `json:"modified_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	// MainlineRef names the remote ref that already has a slot the directory
	// lacks: the directory is behind, the memory itself exists (DENE-1660).
	MainlineRef string `json:"mainline_ref,omitempty"`
}

// Check stats only. It never creates, opens for writing, or changes anything
// under root. A permission or type mismatch is represented in the result so a
// daemon can report a useful reason without aborting the other four checks.
func Check(root string) []LocationResult {
	result := make([]LocationResult, 0, len(locations))
	for _, location := range locations {
		item := LocationResult{Location: location}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(location.Path)))
		if err != nil {
			item.Error = err.Error()
			result = append(result, item)
			continue
		}
		item.IsDirectory = info.IsDir()
		item.Exists = (location.Kind == "directory" && info.IsDir()) ||
			(location.Kind == "file" && !info.IsDir())
		modified := info.ModTime().UTC()
		item.ModifiedAt = &modified
		if !item.Exists {
			item.Error = "found the path with the wrong type"
		}
		result = append(result, item)
	}
	return result
}

// MissingKeys returns stable checklist keys for failed observations.
func MissingKeys(results []LocationResult) []string {
	missing := make([]string, 0, len(results))
	for _, result := range results {
		if !result.Exists {
			missing = append(missing, result.Key)
		}
	}
	sort.Strings(missing)
	return missing
}

// MatchFiles returns the delivered paths that write the checklist slot key.
// A map file counts wherever it sits (a nested AGENTS.md is still the map);
// the indexes and the ADR directory count under any docs/ root, so a
// monorepo package keeping its own docs/ is matched too.
func MatchFiles(key string, files []string) []string {
	var matched []string
	for _, raw := range files {
		file := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(raw)), "./")
		if file == "" {
			continue
		}
		if matchesLocation(key, file) {
			matched = append(matched, file)
		}
	}
	sort.Strings(matched)
	return matched
}

func matchesLocation(key, file string) bool {
	base := path.Base(file)
	underRoot := func(p string) bool { return file == p || strings.HasSuffix(file, "/"+p) }
	switch key {
	case LocationAgents:
		return base == "AGENTS.md"
	case LocationContext:
		return base == "CONTEXT.md"
	case LocationADR:
		return strings.HasPrefix(file, "docs/adr/") || strings.Contains(file, "/docs/adr/")
	case LocationDocs:
		return underRoot("docs/README.md")
	case LocationEvidence:
		return underRoot("docs/evidence/INDEX.md")
	}
	return false
}
