package vault

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex"
)

// FileKind says what a vault file is.
type FileKind string

const (
	KindIndex  FileKind = "index"
	KindEntity FileKind = "entity"
	KindSource FileKind = "source"
)

// File is one rendered markdown page of the vault, held in memory.
type File struct {
	Path       string // forward slashes, with .md: "people/alice-chen.md"
	Content    string // exactly what the exporter writes
	Kind       FileKind
	Folder     string // "people", "sources"; "" for the index
	Name       string // display name
	EntityID   string // entity pages only
	EntityType string // entity pages only
	Updated    time.Time
}

// Set is a user's whole vault rendered in memory. Files are sorted by path
// with the index first.
type Set struct {
	Files []File
	// EntityIDs lists every entity in the graph, including any whose page
	// could not be rendered (those appear in Errors).
	EntityIDs []string
	Errors    []error
}

// Build projects the cortex graph onto an in-memory file set, with the same
// content Export writes to disk. c is already scoped to one user.
func Build(ctx context.Context, c *cortex.Cortex, exportedAt time.Time) (*Set, error) {
	set := &Set{}

	entities, err := c.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		return nil, fmt.Errorf("vault: list entities: %w", err)
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
		set.EntityIDs = append(set.EntityIDs, e.ID)
	}

	sourceEntities := map[string][]sourceEntity{}
	sourceMemories := map[string][]string{}
	sourceUpdated := map[string]time.Time{}
	indexByType := map[string][]indexItem{}

	// First pass: collect every distinct source string referenced anywhere
	// (entity.Source or memory.Source) so we can build a single canonical
	// collision-resolved basename map BEFORE rendering any entity page.
	// This guarantees the [[sources/X]] wikilinks emitted by renderEntity
	// match the filenames the exporter actually writes.
	entityMemories := make(map[string][]cortex.Memory, len(entities))
	entitySources := make(map[string][]string, len(entities))
	allSources := map[string]struct{}{}
	for _, e := range entities {
		memories, err := c.GetMemoriesByEntity(ctx, e.ID)
		if err != nil {
			set.Errors = append(set.Errors, fmt.Errorf("memories for %s: %w", e.ID, err))
			continue
		}
		entityMemories[e.ID] = memories
		sourceSet := map[string]struct{}{}
		if e.Source != "" {
			sourceSet[e.Source] = struct{}{}
			allSources[e.Source] = struct{}{}
		}
		for _, m := range memories {
			if m.Source != "" {
				sourceSet[m.Source] = struct{}{}
				allSources[m.Source] = struct{}{}
			}
		}
		entitySources[e.ID] = keysSorted(sourceSet)
	}
	sourceBasenames := computeSourceBasenames(allSources)

	var indexUpdated time.Time
	for _, e := range entities {
		meta := pageByID[e.ID]
		memories, ok := entityMemories[e.ID]
		if !ok {
			// memories lookup failed in the first pass and was already recorded.
			continue
		}
		rels, err := c.GetRelationships(ctx, e.ID)
		if err != nil {
			set.Errors = append(set.Errors, fmt.Errorf("rels for %s: %w", e.ID, err))
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

		// Resolve each raw source to its collision-aware basename so the
		// rendered wikilinks match the files the exporter will write.
		sourceLinks := make([]string, 0, len(entitySources[e.ID]))
		for _, s := range entitySources[e.ID] {
			if base, ok := sourceBasenames[s]; ok {
				sourceLinks = append(sourceLinks, base)
			}
		}

		updated := e.UpdatedAt
		for _, m := range memories {
			if m.UpdatedAt.After(updated) {
				updated = m.UpdatedAt
			}
		}
		if updated.After(indexUpdated) {
			indexUpdated = updated
		}

		set.Files = append(set.Files, File{
			Path:       meta.path + ".md",
			Content:    renderEntity(e, memories, outRels, inRels, sourceLinks, exportedAt),
			Kind:       KindEntity,
			Folder:     folderForType(e.Type),
			Name:       e.Name,
			EntityID:   e.ID,
			EntityType: e.Type,
			Updated:    updated,
		})

		sourceSet := map[string]struct{}{}
		if e.Source != "" {
			sourceSet[e.Source] = struct{}{}
		}
		for _, m := range memories {
			if m.Source != "" {
				sourceSet[m.Source] = struct{}{}
			}
		}
		for s := range sourceSet {
			sourceEntities[s] = append(sourceEntities[s], sourceEntity{Path: meta.path, Name: e.Name})
			if updated.After(sourceUpdated[s]) {
				sourceUpdated[s] = updated
			}
		}
		for _, m := range memories {
			if m.Source != "" {
				sourceMemories[m.Source] = append(sourceMemories[m.Source], m.Content)
			}
		}

		heading := capitalize(folderForType(e.Type))
		indexByType[heading] = append(indexByType[heading], indexItem{
			Path: meta.path, Name: e.Name, Summary: indexSummary(e),
		})
	}

	// Source pages, one per distinct source, named by the shared collision map.
	for s, ents := range sourceEntities {
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name < ents[j].Name })
		p := sourcePage{Source: s, Entities: ents, Memories: dedupSorted(sourceMemories[s])}
		set.Files = append(set.Files, File{
			Path:    "sources/" + sourceBasenames[s] + ".md",
			Content: renderSource(p, exportedAt),
			Kind:    KindSource,
			Folder:  "sources",
			Name:    s,
			Updated: sourceUpdated[s],
		})
	}

	var groups []indexGroup
	for _, h := range sortedKeysStringSlice(indexByType) {
		items := indexByType[h]
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		groups = append(groups, indexGroup{Heading: h, Items: items})
	}
	set.Files = append(set.Files, File{
		Path:    "index.md",
		Content: renderIndex(groups, exportedAt),
		Kind:    KindIndex,
		Name:    "Index",
		Updated: indexUpdated,
	})

	sort.Slice(set.Files, func(i, j int) bool {
		a, b := set.Files[i], set.Files[j]
		if (a.Kind == KindIndex) != (b.Kind == KindIndex) {
			return a.Kind == KindIndex
		}
		return a.Path < b.Path
	})
	return set, nil
}
