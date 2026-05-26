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

	entities, err := c.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		return stats, fmt.Errorf("vault: list entities: %w", err)
	}

	sort.Slice(entities, func(i, j int) bool { return entities[i].ID < entities[j].ID })
	folderSlugCount := map[string]int{}
	for _, e := range entities {
		k := folderForType(e.Type) + "/" + slug(e.Name)
		folderSlugCount[k]++
	}

	type pageMeta struct {
		ent  cortex.Entity
		path string // "people/alice-chen" (no .md)
	}
	pageByID := make(map[string]pageMeta, len(entities))
	for _, e := range entities {
		colliding := folderSlugCount[folderForType(e.Type)+"/"+slug(e.Name)] > 1
		fn := pageFilename(e.Name, e.ID, colliding)
		pageByID[e.ID] = pageMeta{
			ent:  e,
			path: folderForType(e.Type) + "/" + strings.TrimSuffix(fn, ".md"),
		}
	}

	exportedAt := time.Now().UTC()

	sourceEntities := map[string][]sourceEntity{}
	sourceMemories := map[string][]string{}
	indexByType := map[string][]indexItem{}

	for _, e := range entities {
		meta := pageByID[e.ID]
		memories, err := c.GetMemoriesByEntity(ctx, e.ID)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Errorf("memories for %s: %w", e.ID, err))
			continue
		}
		rels, err := c.GetRelationships(ctx, e.ID)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Errorf("rels for %s: %w", e.ID, err))
			continue
		}

		var outRels, inRels []resolvedRel
		for _, r := range rels {
			if r.SourceID == e.ID {
				other, ok := pageByID[r.TargetID]
				if !ok {
					continue
				}
				outRels = append(outRels, resolvedRel{Type: r.Type, OtherPath: other.path, OtherName: other.ent.Name})
			} else {
				other, ok := pageByID[r.SourceID]
				if !ok {
					continue
				}
				inRels = append(inRels, resolvedRel{Type: r.Type, OtherPath: other.path, OtherName: other.ent.Name})
			}
		}

		sourceSet := map[string]struct{}{}
		if e.Source != "" {
			sourceSet[e.Source] = struct{}{}
		}
		for _, m := range memories {
			if m.Source != "" {
				sourceSet[m.Source] = struct{}{}
			}
		}
		sources := keysSorted(sourceSet)

		content := renderEntity(e, memories, outRels, inRels, sources, exportedAt)
		hash := hashContent(content)
		entry, exists := manifest.Pages[e.ID]
		desiredRel := meta.path + ".md"

		if exists && entry.ContentHash == hash && entry.Path == desiredRel && !opts.Full {
			stats.Skipped++
		} else {
			if !opts.DryRun {
				if exists && entry.Path != desiredRel {
					_ = os.Remove(filepath.Join(opts.VaultDir, entry.Path))
				}
				outPath := filepath.Join(opts.VaultDir, desiredRel)
				if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
					stats.Errors = append(stats.Errors, fmt.Errorf("mkdir %s: %w", filepath.Dir(outPath), err))
					continue
				}
				if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
					stats.Errors = append(stats.Errors, fmt.Errorf("write %s: %w", outPath, err))
					continue
				}
			}
			manifest.Pages[e.ID] = PageEntry{
				Path:        desiredRel,
				ContentHash: hash,
				ExportedAt:  exportedAt,
			}
			stats.Written++
		}

		for s := range sourceSet {
			sourceEntities[s] = append(sourceEntities[s], sourceEntity{Path: meta.path, Name: e.Name})
		}
		for _, m := range memories {
			if m.Source != "" {
				sourceMemories[m.Source] = append(sourceMemories[m.Source], m.Content)
			}
		}

		heading := capitalize(folderForType(e.Type))
		summary := indexSummary(e)
		indexByType[heading] = append(indexByType[heading], indexItem{
			Path: meta.path, Name: e.Name, Summary: summary,
		})
	}

	// Archive stale pages: any manifest entry whose ID is no longer in the graph.
	if len(entities) > 0 || opts.Full {
		liveIDs := map[string]struct{}{}
		for _, e := range entities {
			liveIDs[e.ID] = struct{}{}
		}
		var staleIDs []string
		for id := range manifest.Pages {
			if _, ok := liveIDs[id]; !ok {
				staleIDs = append(staleIDs, id)
			}
		}
		if len(staleIDs) > 0 {
			archiveDir := filepath.Join(opts.VaultDir, "_archive", exportedAt.Format("2006-01-02T15-04-05"))
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
	}

	if !opts.DryRun {
		sourcesDir := filepath.Join(opts.VaultDir, "sources")
		if err := os.MkdirAll(sourcesDir, 0o755); err != nil {
			stats.Errors = append(stats.Errors, err)
		} else {
			for s, ents := range sourceEntities {
				sort.Slice(ents, func(i, j int) bool { return ents[i].Name < ents[j].Name })
				p := sourcePage{Source: s, Entities: ents, Memories: dedupSorted(sourceMemories[s])}
				content := renderSource(p, exportedAt)
				outPath := filepath.Join(sourcesDir, slug(s)+".md")
				if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
					stats.Errors = append(stats.Errors, err)
				}
			}
		}
	}

	if !opts.DryRun {
		var groups []indexGroup
		for _, h := range sortedKeysStringSlice(indexByType) {
			items := indexByType[h]
			sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
			groups = append(groups, indexGroup{Heading: h, Items: items})
		}
		content := renderIndex(groups, exportedAt)
		if err := os.WriteFile(filepath.Join(opts.VaultDir, "index.md"), []byte(content), 0o644); err != nil {
			stats.Errors = append(stats.Errors, err)
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
