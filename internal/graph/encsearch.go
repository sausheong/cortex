package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/llmutil"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
	"github.com/sausheong/cortex/internal/workset"
)

// In-memory entity search for encrypted (and migrating) users: names, types
// and sources are ciphertext, so filtering runs over the working set. A
// keyless handle gets model.ErrLocked, never an empty list.

// searchEntitiesEnc is SearchEntities over the working set. The query is a
// literal substring (wildcards carry no meaning), names starting with it
// first, then by name and id.
func (c Ops) searchEntitiesEnc(ctx context.Context, q string, types []string, excludeID string, limit int) ([]model.Entity, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	lq := strings.ToLower(q)
	var typeSet map[string]bool
	if len(types) > 0 {
		typeSet = make(map[string]bool, len(types))
		for _, t := range types {
			typeSet[t] = true
		}
	}
	type hit struct {
		e      model.Entity
		prefix bool
	}
	var hits []hit
	for _, e := range ws.Entities() {
		if e.ID == excludeID {
			continue
		}
		if typeSet != nil && !typeSet[e.Type] {
			continue
		}
		ln := strings.ToLower(e.Name)
		if !strings.Contains(ln, lq) {
			continue
		}
		hits = append(hits, hit{model.Entity{ID: e.ID, Type: e.Type, Name: e.Name, Source: e.Source}, strings.HasPrefix(ln, lq)})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].prefix != hits[j].prefix {
			return hits[i].prefix
		}
		if hits[i].e.Name != hits[j].e.Name {
			return hits[i].e.Name < hits[j].e.Name
		}
		return hits[i].e.ID < hits[j].e.ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	var out []model.Entity
	for _, h := range hits {
		out = append(out, h.e)
	}
	return out, nil
}

// findEntitiesEnc is FindEntities over the working set.
func (c Ops) findEntitiesEnc(ctx context.Context, f model.EntityFilter) ([]model.Entity, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	var out []model.Entity
	for _, e := range ws.Entities() {
		if f.Type != "" && ontology.NormalizeType(e.Type) != ontology.NormalizeType(f.Type) {
			continue
		}
		if f.NameLike != "" && !ilikeMatch(f.NameLike, e.Name) {
			continue
		}
		if f.Source != "" && e.Source != f.Source {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// memoryIDsContaining lists the live memories whose content contains q,
// case-insensitively and literally (the ILIKE of the legacy path with its
// wildcards escaped). Never nil, so it binds as an empty array.
func (c Ops) memoryIDsContaining(ctx context.Context, q string) ([]string, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	lq := strings.ToLower(q)
	ids := []string{}
	for _, m := range ws.Memories() {
		if strings.Contains(strings.ToLower(m.Content), lq) {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// sampleMemoriesEnc selects the matching ids in SQL (plain columns only) and
// takes the decrypted content from the working set.
func (c Ops) sampleMemoriesEnc(ctx context.Context, where string, args []any, limit int) ([]model.Memory, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	rows, err := c.DB.QueryContext(ctx,
		`SELECT id, created_at FROM memories WHERE `+where+` ORDER BY created_at DESC, id LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("cortex: sample memories: %w", err)
	}
	defer rows.Close()
	var out []model.Memory
	for rows.Next() {
		var m model.Memory
		if err := rows.Scan(&m.ID, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
		}
		if full, ok := ws.MemoryByID(m.ID); ok {
			m.Content = full.Content
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// lintEnc fills the content-dependent parts of a lint report from the working
// set (names, types, sources and memory text are ciphertext). Which entities
// have memory links comes from the plain link table, exactly as the SQL does.
func (c Ops) lintEnc(ctx context.Context, cfg *model.LintConfig, r *model.LintReport) error {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return err
	}
	linked := map[string]bool{}
	rows, err := c.DB.QueryContext(ctx, `SELECT DISTINCT entity_id FROM memory_entities WHERE user_id = ?`, c.UserID)
	if err != nil {
		return fmt.Errorf("cortex: lint: memory links: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		linked[id] = true
	}
	rows.Close()
	hasRel := map[string]bool{}
	for _, rel := range ws.Relationships() {
		hasRel[rel.SourceID], hasRel[rel.TargetID] = true, true
	}
	ents := ws.Entities()
	byTypeName := func(l []model.EntityRef) {
		sort.SliceStable(l, func(i, j int) bool {
			if l[i].Type != l[j].Type {
				return l[i].Type < l[j].Type
			}
			return l[i].Name < l[j].Name
		})
	}
	for _, e := range ents {
		ref := model.EntityRef{ID: e.ID, Name: e.Name, Type: e.Type}
		switch {
		case !hasRel[e.ID] && !linked[e.ID]:
			r.Orphans = append(r.Orphans, ref)
		case !linked[e.ID] && hasRel[e.ID]:
			r.EntitiesNoMemories = append(r.EntitiesNoMemories, ref)
		}
	}
	byTypeName(r.Orphans)
	byTypeName(r.EntitiesNoMemories)

	// Near duplicates: same pairing as the SQL path, over entities by id.
	sorted := append([]model.Entity(nil), ents...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	groups := map[string][]model.EntityRef{}
	var types []string
	for _, e := range sorted {
		t := ontology.NormalizeType(e.Type)
		if _, ok := groups[t]; !ok {
			types = append(types, t)
		}
		groups[t] = append(groups[t], model.EntityRef{ID: e.ID, Name: e.Name, Type: e.Type})
	}
	sort.Strings(types)
	for _, t := range types {
		g := groups[t]
		if len(g) > nearDuplicateGroupCap {
			g = g[:nearDuplicateGroupCap]
		}
		var pairs []model.DuplicatePair
		for i := 0; i < len(g); i++ {
			for j := i + 1; j < len(g); j++ {
				if s := NameSimilarity(g[i].Name, g[j].Name); s >= nearDuplicateMin {
					pairs = append(pairs, model.DuplicatePair{Type: t, A: g[i], B: g[j], Score: s})
				}
			}
		}
		sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Score > pairs[j].Score })
		r.NearDuplicates = append(r.NearDuplicates, pairs...)
	}

	entSources := map[string]bool{}
	for _, e := range ents {
		if e.Source != "" {
			entSources[e.Source] = true
		}
	}
	mems := ws.Memories()
	deadSet := map[string]bool{}
	for _, m := range mems {
		if m.Source != "" && !entSources[m.Source] {
			deadSet[m.Source] = true
		}
	}
	for s := range deadSet {
		r.DeadSources = append(r.DeadSources, s)
	}
	sort.Strings(r.DeadSources)

	sort.SliceStable(mems, func(i, j int) bool { return mems[i].CreatedAt.After(mems[j].CreatedAt) })
	ref := func(m model.Memory) model.MemoryRef {
		return model.MemoryRef{ID: m.ID, Content: llmutil.Truncate(m.Content, 80), Source: m.Source, Confidence: m.Confidence}
	}
	for _, m := range mems {
		if len(m.EntityIDs) == 0 {
			r.UnlinkedMemories = append(r.UnlinkedMemories, ref(m))
		}
	}
	if cfg.LowConfidence {
		var low []model.Memory
		for _, m := range mems {
			if m.Confidence < cfg.LowConfidenceThreshold {
				low = append(low, m)
			}
		}
		sort.SliceStable(low, func(i, j int) bool { return low[i].Confidence < low[j].Confidence })
		for _, m := range low {
			r.LowConfidenceMemories = append(r.LowConfidenceMemories, ref(m))
		}
	}
	return nil
}

// graphNeighbourhoodEnc fills g (which holds the centre) with the centre's
// neighbours and the edges among the chosen nodes, from the working set. The
// ranking and truncation rules mirror GraphNeighbourhood's SQL.
func (c Ops) graphNeighbourhoodEnc(ctx context.Context, g *model.Neighbourhood, centerID string, f model.GraphFilter, limit int) error {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return err
	}
	type nb struct {
		e    model.Entity
		n    int
		last time.Time
	}
	cands := map[string]*nb{}
	rels := ws.Relationships()
	for _, r := range rels {
		if r.SourceID != centerID && r.TargetID != centerID {
			continue
		}
		other := r.SourceID
		if r.SourceID == centerID {
			other = r.TargetID
		}
		if other == centerID {
			continue
		}
		e, ok := ws.EntityByID(other)
		if !ok {
			continue
		}
		x := cands[other]
		if x == nil {
			x = &nb{e: e}
			cands[other] = x
		}
		x.n++
		if r.CreatedAt.After(x.last) {
			x.last = r.CreatedAt
		}
	}
	var typeSet map[string]bool
	if len(f.Types) > 0 {
		typeSet = map[string]bool{}
		for _, t := range f.Types {
			typeSet[t] = true
		}
	}
	var recent map[string]bool
	if f.Since != nil {
		recent = map[string]bool{}
		for _, m := range ws.Memories() {
			if !m.CreatedAt.Before(*f.Since) {
				for _, id := range m.EntityIDs {
					recent[id] = true
				}
			}
		}
	}
	var list []*nb
	for _, x := range cands {
		if typeSet != nil && !typeSet[x.e.Type] {
			continue
		}
		if recent != nil && !recent[x.e.ID] {
			continue
		}
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch {
		case a.n != b.n:
			return a.n > b.n
		case !a.last.Equal(b.last):
			return a.last.After(b.last)
		case a.e.Name != b.e.Name:
			return a.e.Name < b.e.Name
		}
		return a.e.ID < b.e.ID
	})
	if len(list) > limit {
		list = list[:limit]
	}
	ids := map[string]bool{centerID: true}
	for _, x := range list {
		if len(g.Nodes) >= limit {
			g.Truncated = true
			break
		}
		g.Nodes = append(g.Nodes, model.GraphNode{ID: x.e.ID, Type: x.e.Type, Name: x.e.Name, Rels: x.n})
		ids[x.e.ID] = true
	}
	var edges []model.Relationship
	for _, r := range rels {
		if ids[r.SourceID] && ids[r.TargetID] {
			edges = append(edges, r)
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if !edges[i].CreatedAt.Equal(edges[j].CreatedAt) {
			return edges[i].CreatedAt.After(edges[j].CreatedAt)
		}
		return edges[i].ID < edges[j].ID
	})
	if len(edges) > limit*6 {
		edges = edges[:limit*6]
	}
	for _, r := range edges {
		g.Edges = append(g.Edges, model.GraphEdge{ID: r.ID, SourceID: r.SourceID, TargetID: r.TargetID, Type: r.Type})
	}
	return nil
}

// entityTypesEnc lists the distinct types of live entities, sorted.
func (c Ops) entityTypesEnc(ctx context.Context) ([]string, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range ws.Entities() {
		if !seen[e.Type] {
			seen[e.Type] = true
			out = append(out, e.Type)
		}
	}
	sort.Strings(out)
	return out, nil
}

// entityRelationsEnc is EntityRelations over the working set.
func (c Ops) entityRelationsEnc(ctx context.Context, id string, limit int) ([]model.EntityRelation, int, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, 0, err
	}
	var rels []model.Relationship
	for _, r := range ws.Relationships() {
		if r.SourceID != id && r.TargetID != id {
			continue
		}
		other := r.SourceID
		if r.SourceID == id {
			other = r.TargetID
		}
		if _, ok := ws.EntityByID(other); !ok {
			continue
		}
		rels = append(rels, r)
	}
	sort.Slice(rels, func(i, j int) bool {
		if !rels[i].CreatedAt.Equal(rels[j].CreatedAt) {
			return rels[i].CreatedAt.After(rels[j].CreatedAt)
		}
		return rels[i].ID < rels[j].ID
	})
	total := len(rels)
	if len(rels) > limit {
		rels = rels[:limit]
	}
	var out []model.EntityRelation
	for _, r := range rels {
		other := r.SourceID
		if r.SourceID == id {
			other = r.TargetID
		}
		o, _ := ws.EntityByID(other)
		out = append(out, model.EntityRelation{ID: r.ID, Type: r.Type, Outgoing: r.SourceID == id,
			OtherID: o.ID, OtherName: o.Name, OtherType: o.Type, CreatedAt: r.CreatedAt})
	}
	return out, total, nil
}
