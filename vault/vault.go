package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex"
)

// Options controls Export.
type Options struct {
	VaultDir string // required
	Full     bool   // false = incremental (skip unchanged pages)
	DryRun   bool   // compute stats but write nothing
}

// Stats reports what Export did (or would do, under DryRun).
type Stats struct {
	Written  int
	Skipped  int
	Archived int
	Errors   []error
}

// Export projects the cortex graph onto a markdown vault.
// See docs/superpowers/specs/2026-05-26-vault-export-and-cortex-schema-design.md
// for the full algorithm.
func Export(ctx context.Context, c *cortex.Cortex, opts Options) (Stats, error) {
	var stats Stats

	if opts.VaultDir == "" {
		return stats, fmt.Errorf("vault: VaultDir is required")
	}
	if !opts.DryRun {
		if err := os.MkdirAll(opts.VaultDir, 0o755); err != nil {
			return stats, fmt.Errorf("vault: create dir: %w", err)
		}
	}

	manifestPath := filepath.Join(opts.VaultDir, ManifestFilename)
	manifest, err := loadManifest(manifestPath)
	if err != nil && !opts.Full {
		return stats, err
	}
	if opts.Full {
		manifest = newManifest()
	}

	exportedAt := time.Now().UTC()
	archiveStamp := exportedAt.Format("2006-01-02T15-04-05")

	set, err := Build(ctx, c, exportedAt)
	if err != nil {
		return stats, err
	}
	stats.Errors = append(stats.Errors, set.Errors...)

	for _, f := range set.Files {
		if f.Kind != KindEntity {
			continue
		}
		hash := hashContent(f.Content)
		entry, exists := manifest.Pages[f.EntityID]
		desiredRel := f.Path

		if exists && entry.ContentHash == hash && entry.Path == desiredRel && !opts.Full {
			stats.Skipped++
			continue
		}
		if !opts.DryRun {
			if exists && entry.Path != desiredRel {
				_ = os.Remove(filepath.Join(opts.VaultDir, entry.Path))
			}
			outPath := filepath.Join(opts.VaultDir, desiredRel)
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				stats.Errors = append(stats.Errors, fmt.Errorf("mkdir %s: %w", filepath.Dir(outPath), err))
				continue
			}
			if err := os.WriteFile(outPath, []byte(f.Content), 0o644); err != nil {
				stats.Errors = append(stats.Errors, fmt.Errorf("write %s: %w", outPath, err))
				continue
			}
		}
		manifest.Pages[f.EntityID] = PageEntry{
			Path:        desiredRel,
			ContentHash: hash,
			ExportedAt:  exportedAt,
		}
		stats.Written++
	}

	// Archive stale pages: any manifest entry whose ID is no longer in the graph.
	liveIDs := map[string]struct{}{}
	for _, id := range set.EntityIDs {
		liveIDs[id] = struct{}{}
	}
	var staleIDs []string
	for id := range manifest.Pages {
		if _, ok := liveIDs[id]; !ok {
			staleIDs = append(staleIDs, id)
		}
	}
	if len(staleIDs) > 0 {
		archiveDir := filepath.Join(opts.VaultDir, "_archive", archiveStamp)
		for _, id := range staleIDs {
			entry := manifest.Pages[id]
			if !opts.DryRun {
				src := filepath.Join(opts.VaultDir, entry.Path)
				dst := filepath.Join(archiveDir, entry.Path)
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					stats.Errors = append(stats.Errors, err)
					continue
				}
				if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
					stats.Errors = append(stats.Errors, err)
					continue
				}
			}
			delete(manifest.Pages, id)
			stats.Archived++
		}
	}

	if !opts.DryRun {
		sourcesDir := filepath.Join(opts.VaultDir, "sources")
		if err := os.MkdirAll(sourcesDir, 0o755); err != nil {
			stats.Errors = append(stats.Errors, err)
		} else {
			// Compute the set of source filenames we will write this run.
			liveSources := map[string]struct{}{}
			for _, f := range set.Files {
				if f.Kind == KindSource {
					liveSources[strings.TrimPrefix(f.Path, "sources/")] = struct{}{}
				}
			}
			// Archive existing source files that won't be written this run.
			existing, _ := os.ReadDir(sourcesDir)
			for _, ent := range existing {
				if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
					continue
				}
				if _, live := liveSources[ent.Name()]; live {
					continue
				}
				archiveDir := filepath.Join(opts.VaultDir, "_archive", archiveStamp, "sources")
				if err := os.MkdirAll(archiveDir, 0o755); err != nil {
					stats.Errors = append(stats.Errors, err)
					continue
				}
				src := filepath.Join(sourcesDir, ent.Name())
				dst := filepath.Join(archiveDir, ent.Name())
				if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
					stats.Errors = append(stats.Errors, err)
					continue
				}
				stats.Archived++
			}
			for _, f := range set.Files {
				if f.Kind != KindSource {
					continue
				}
				outPath := filepath.Join(opts.VaultDir, f.Path)
				if err := os.WriteFile(outPath, []byte(f.Content), 0o644); err != nil {
					stats.Errors = append(stats.Errors, err)
				}
			}
		}
	}

	if !opts.DryRun {
		for _, f := range set.Files {
			if f.Kind != KindIndex {
				continue
			}
			if err := os.WriteFile(filepath.Join(opts.VaultDir, f.Path), []byte(f.Content), 0o644); err != nil {
				stats.Errors = append(stats.Errors, err)
			}
		}
	}

	if !opts.DryRun {
		summary := fmt.Sprintf("%d pages written, %d skipped, %d archived", stats.Written, stats.Skipped, stats.Archived)
		line := formatLogLine(exportedAt, "export", summary)
		f, err := os.OpenFile(filepath.Join(opts.VaultDir, "log.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			stats.Errors = append(stats.Errors, err)
		} else {
			_, _ = f.WriteString(line)
			_ = f.Close()
		}
	}

	if !opts.DryRun {
		manifest.ExportedAt = exportedAt
		if err := saveManifest(manifestPath, manifest); err != nil {
			stats.Errors = append(stats.Errors, err)
		}
	}

	return stats, nil
}

// computeSourceBasenames produces, for each distinct source string, the
// filename (without ".md" extension or directory) that the exporter will
// write into vault/sources/. When two source strings slug to the same base,
// each gets a "-<6hex>" suffix derived from the raw source string so the
// distinct sources are stored in distinct files. Returns an empty map if
// the input is empty.
func computeSourceBasenames(sources map[string]struct{}) map[string]string {
	out := make(map[string]string, len(sources))
	if len(sources) == 0 {
		return out
	}
	slugCounts := map[string]int{}
	for s := range sources {
		slugCounts[slug(s)]++
	}
	for s := range sources {
		base := slug(s)
		if base == "" {
			// Pathological: source string with no alphanumeric. Use a short hash.
			h := sha256.Sum256([]byte(s))
			base = hex.EncodeToString(h[:3])
		}
		name := base
		if slugCounts[slug(s)] > 1 {
			h := sha256.Sum256([]byte(s))
			name = base + "-" + hex.EncodeToString(h[:3])
		}
		out[s] = name
	}
	return out
}

func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func keysSorted(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysStringSlice(m map[string][]indexItem) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, s := range in {
		seen[s] = struct{}{}
	}
	out := keysSorted(seen)
	return out
}

// indexSummary derives a one-line summary for an index entry from the
// entity's attributes. Returns "" if no useful summary can be built.
func indexSummary(e cortex.Entity) string {
	role, _ := e.Attributes["role"].(string)
	team, _ := e.Attributes["team"].(string)
	switch {
	case role != "" && team != "":
		return role + " at " + team
	case role != "":
		return role
	case team != "":
		return team
	}
	return ""
}

// capitalize uppercases the first rune of s; the rest is unchanged.
// Replaces strings.Title (deprecated in Go 1.18+); avoids the
// golang.org/x/text dependency for a trivially small need.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
