package cortex

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// NormalizeReport describes what NormalizeGraph changed (or, in a dry run,
// would change).
type NormalizeReport struct {
	DryRun bool `json:"dry_run"`
	// BatchID identifies the batch that RestoreBatch uses to undo the run.
	BatchID string `json:"batch_id,omitempty"`
	// Retyped lists entities whose type label moved onto the curated set.
	Retyped []RetypedEntity `json:"retyped"`
	// Merged lists entities folded into another of the same name and kind,
	// including duplicates of the owner.
	Merged []MergedEntity `json:"merged"`
	// RelationshipsRetyped counts edges whose verb moved onto the curated set
	// (possibly swapping ends); RelationshipsFolded counts edges absorbed into
	// an identical live edge.
	RelationshipsRetyped int `json:"relationships_retyped"`
	RelationshipsFolded  int `json:"relationships_folded"`
	// UnmappedVerbs counts verbs with no curated equivalent, now stored as
	// related_to with the original kept as the "label" attribute.
	UnmappedVerbs map[string]int `json:"unmapped_verbs,omitempty"`
}

// RetypedEntity is one entity whose type changed.
type RetypedEntity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// MergedEntity is one entity merged into another.
type MergedEntity struct {
	KeepID   string `json:"keep_id"`
	DropID   string `json:"drop_id"`
	Name     string `json:"name"`
	DropName string `json:"drop_name"`
	Type     string `json:"type"`
}

type NormalizeOption func(*normalizeConfig)
type normalizeConfig struct{ dryRun bool }

// WithNormalizeDryRun reports what NormalizeGraph would change without
// writing anything.
func WithNormalizeDryRun() NormalizeOption { return func(c *normalizeConfig) { c.dryRun = true } }

// NormalizeGraph brings an existing graph onto the curated vocabulary, in one
// transaction recorded as a single undoable batch:
//
//  1. every entity type moves onto the curated set (an unknown label becomes
//     concept, keeping the label in the "type_label" attribute);
//  2. live entities that now share a name (ignoring case) and type are
//     merged, as are person entities named like the owner, into the owner;
//  3. every relationship verb moves onto the curated set (CanonicalRelation),
//     folding any edge that becomes identical to a live one.
//
// RestoreBatch with the report's BatchID undoes the whole run.
func (c *Cortex) NormalizeGraph(ctx context.Context, opts ...NormalizeOption) (NormalizeReport, error) {
	cfg := &normalizeConfig{}
	for _, o := range opts {
		o(cfg)
	}
	report := NormalizeReport{DryRun: cfg.dryRun, UnmappedVerbs: map[string]int{}}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("cortex: begin normalize: %w", err)
	}
	defer tx.Rollback()
	batchID, err := newBatch(ctx, tx, c.userID, "normalize")
	if err != nil {
		return report, err
	}
	mo := mergeOpts{batchID: batchID, by: "normalize", sameKind: true}

	ownerID, _ := c.OwnerEntityID(ctx)
	ownerNames := map[string]bool{}
	if ownerID != "" {
		_, ownerNames = c.owner(ctx, &resolveContext{})
	}

	// 1. Retype entities.
	type ent struct {
		id, name, typ, orig string
		attrs               map[string]any
		labelled            bool // gained a type_label
		links               int
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT e.id, e.name, e.type, COALESCE(e.attributes, 'null'::jsonb)::text,
		        (SELECT count(*) FROM memory_entities me WHERE me.user_id = e.user_id AND me.entity_id = e.id)
		 FROM entities e WHERE e.user_id = ? AND e.deleted_at IS NULL ORDER BY e.created_at, e.id`, c.userID)
	if err != nil {
		return report, fmt.Errorf("cortex: normalize scan entities: %w", err)
	}
	var ents []*ent
	for rows.Next() {
		e := &ent{}
		var attrs string
		if err := rows.Scan(&e.id, &e.name, &e.typ, &attrs, &e.links); err != nil {
			rows.Close()
			return report, fmt.Errorf("cortex: normalize scan entity: %w", err)
		}
		_ = json.Unmarshal([]byte(attrs), &e.attrs)
		e.orig = e.typ
		ents = append(ents, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("cortex: normalize scan entities: %w", err)
	}

	ownerLive := false
	for _, e := range ents {
		ownerLive = ownerLive || e.id == ownerID
	}

	// Group by (canonical type, lower name) to find who survives a collision.
	groups := map[string][]*ent{}
	var keys []string
	for _, e := range ents {
		canonical, known := CanonicalEntityType(e.typ)
		if !known {
			if label := NormalizeType(e.typ); label != "" && label != TypeConcept {
				if e.attrs == nil {
					e.attrs = map[string]any{}
				}
				if _, ok := e.attrs["type_label"]; !ok {
					e.attrs["type_label"] = label
					e.labelled = true
				}
			}
		}
		if canonical != e.typ {
			report.Retyped = append(report.Retyped, RetypedEntity{ID: e.id, Name: e.name, From: e.typ, To: canonical})
		}
		e.typ = canonical
		k := canonical + "\x00" + strings.ToLower(strings.TrimSpace(e.name))
		if canonical == TypePerson && ownerLive && ownerNames[normalizeAlias(AliasName, e.name)] {
			k = "owner"
		}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], e)
	}

	// 2. Per group: pick the keeper, merge the rest into it, then write the
	// keeper's curated type. Merging first means no write ever collides with
	// the (user, name, type) uniqueness of live entities.
	for _, k := range keys {
		g := groups[k]
		if len(g) == 0 {
			continue
		}
		sort.SliceStable(g, func(i, j int) bool {
			if (g[i].id == ownerID) != (g[j].id == ownerID) {
				return g[i].id == ownerID
			}
			return g[i].links > g[j].links
		})
		keep := g[0]
		for _, drop := range g[1:] {
			if _, err := mergeEntitiesTx(ctx, tx, c.userID, keep.id, drop.id, mo); err != nil {
				return report, fmt.Errorf("cortex: normalize merge %q into %q: %w", drop.name, keep.name, err)
			}
			report.Merged = append(report.Merged, MergedEntity{KeepID: keep.id, DropID: drop.id, Name: keep.name, DropName: drop.name, Type: keep.typ})
		}
		if keep.typ == keep.orig && !keep.labelled {
			continue
		}
		if err := recordChanges(ctx, tx, mo, c.userID, "entities", "update", "{}", "t.id = ? AND t.user_id = ?", keep.id, c.userID); err != nil {
			return report, err
		}
		// Re-read attributes: the merges above may have added provenance.
		var attrs string
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(attributes, 'null'::jsonb)::text FROM entities WHERE id = ? AND user_id = ?`, keep.id, c.userID).Scan(&attrs); err != nil {
			return report, fmt.Errorf("cortex: normalize reload %q: %w", keep.name, err)
		}
		var current map[string]any
		_ = json.Unmarshal([]byte(attrs), &current)
		if current == nil {
			current = map[string]any{}
		}
		if tl, ok := keep.attrs["type_label"]; ok {
			if _, has := current["type_label"]; !has {
				current["type_label"] = tl
			}
		}
		attrsJSON, err := json.Marshal(current)
		if err != nil {
			return report, fmt.Errorf("cortex: marshal attributes: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entities SET type = ?, attributes = ?::jsonb WHERE id = ? AND user_id = ?`,
			keep.typ, string(attrsJSON), keep.id, c.userID); err != nil {
			return report, fmt.Errorf("cortex: normalize retype %q: %w", keep.name, err)
		}
	}

	// 3. Retype relationships against their ends' (now curated) types.
	type rel struct {
		id, src, tgt, typ, srcType, tgtType string
		attrs                               map[string]any
		evidence                            int
	}
	rrows, err := tx.QueryContext(ctx,
		`SELECT r.id, r.source_id, r.target_id, r.type, COALESCE(r.attributes, 'null'::jsonb)::text, r.evidence_count, s.type, t.type
		 FROM relationships r
		 JOIN entities s ON s.id = r.source_id
		 JOIN entities t ON t.id = r.target_id
		 WHERE r.user_id = ? AND r.deleted_at IS NULL ORDER BY r.created_at, r.id`, c.userID)
	if err != nil {
		return report, fmt.Errorf("cortex: normalize scan relationships: %w", err)
	}
	var rels []rel
	for rrows.Next() {
		var r rel
		var attrs string
		if err := rrows.Scan(&r.id, &r.src, &r.tgt, &r.typ, &attrs, &r.evidence, &r.srcType, &r.tgtType); err != nil {
			rrows.Close()
			return report, fmt.Errorf("cortex: normalize scan relationship: %w", err)
		}
		_ = json.Unmarshal([]byte(attrs), &r.attrs)
		rels = append(rels, r)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return report, fmt.Errorf("cortex: normalize scan relationships: %w", err)
	}
	for _, r := range rels {
		verb, swap, label := CanonicalRelation(r.typ, r.srcType, r.tgtType)
		src, tgt := r.src, r.tgt
		if swap {
			src, tgt = tgt, src
		}
		if verb == r.typ && !swap {
			continue
		}
		if verb == RelRelatedTo && label != "" {
			report.UnmappedVerbs[label]++
		}
		attrs := r.attrs
		if attrs == nil {
			attrs = map[string]any{}
		}
		if label != "" {
			if _, ok := attrs["label"]; !ok {
				attrs["label"] = label
			}
		}
		// An identical live edge absorbs this one.
		var twin string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM relationships WHERE user_id = ? AND deleted_at IS NULL
			   AND source_id = ? AND target_id = ? AND type = ? AND id <> ?`,
			c.userID, src, tgt, verb, r.id).Scan(&twin)
		if err == nil {
			if err := recordChanges(ctx, tx, mo, c.userID, "relationships", "update", "{}", "t.id = ? AND t.user_id = ?", twin, c.userID); err != nil {
				return report, err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE relationships SET evidence_count = evidence_count + ? WHERE id = ? AND user_id = ?`,
				r.evidence, twin, c.userID); err != nil {
				return report, fmt.Errorf("cortex: normalize fold relationship: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE relationships SET deleted_at = now(), deleted_by = 'normalize', delete_batch_id = ? WHERE id = ? AND user_id = ?`,
				batchID, r.id, c.userID); err != nil {
				return report, fmt.Errorf("cortex: normalize drop folded relationship: %w", err)
			}
			report.RelationshipsFolded++
			continue
		}
		attrsJSON, err := json.Marshal(attrs)
		if err != nil {
			return report, fmt.Errorf("cortex: marshal relationship attributes: %w", err)
		}
		if err := recordChanges(ctx, tx, mo, c.userID, "relationships", "update", "{}", "t.id = ? AND t.user_id = ?", r.id, c.userID); err != nil {
			return report, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE relationships SET type = ?, source_id = ?, target_id = ?, attributes = ?::jsonb WHERE id = ? AND user_id = ?`,
			verb, src, tgt, string(attrsJSON), r.id, c.userID); err != nil {
			return report, fmt.Errorf("cortex: normalize retype relationship: %w", err)
		}
		report.RelationshipsRetyped++
	}

	dropped := map[string]bool{}
	for _, m := range report.Merged {
		dropped[m.DropID] = true
	}
	kept := report.Retyped[:0]
	for _, r := range report.Retyped {
		if !dropped[r.ID] {
			kept = append(kept, r)
		}
	}
	report.Retyped = kept
	if len(report.UnmappedVerbs) == 0 {
		report.UnmappedVerbs = nil
	}
	if cfg.dryRun {
		return report, nil // deferred Rollback discards everything
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("cortex: commit normalize: %w", err)
	}
	report.BatchID = batchID
	return report, nil
}

// RenderNormalizeMarkdown renders a NormalizeReport for the CLI.
func RenderNormalizeMarkdown(r NormalizeReport) string {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("# Graph normalisation (dry run — nothing written)\n\n")
	} else {
		b.WriteString("# Graph normalisation\n\n")
	}
	fmt.Fprintf(&b, "- %d entities retyped\n- %d entities merged\n- %d relationships retyped\n- %d relationships folded into an identical edge\n",
		len(r.Retyped), len(r.Merged), r.RelationshipsRetyped, r.RelationshipsFolded)
	if r.BatchID != "" {
		fmt.Fprintf(&b, "\nUndo with the deleted-items page or `RestoreBatch(%q)`.\n", r.BatchID)
	}
	if len(r.Merged) > 0 {
		b.WriteString("\n## Merged\n\n")
		for _, m := range r.Merged {
			fmt.Fprintf(&b, "- %q into %q (%s): `%s` → `%s`\n", m.DropName, m.Name, m.Type, m.DropID, m.KeepID)
		}
	}
	if len(r.Retyped) > 0 {
		b.WriteString("\n## Retyped\n\n")
		for _, t := range r.Retyped {
			fmt.Fprintf(&b, "- %q: %s → %s\n", t.Name, t.From, t.To)
		}
	}
	if len(r.UnmappedVerbs) > 0 {
		b.WriteString("\n## Verbs with no curated equivalent (now related_to, original kept as label)\n\n")
		verbs := make([]string, 0, len(r.UnmappedVerbs))
		for v := range r.UnmappedVerbs {
			verbs = append(verbs, v)
		}
		sort.Slice(verbs, func(i, j int) bool {
			if r.UnmappedVerbs[verbs[i]] != r.UnmappedVerbs[verbs[j]] {
				return r.UnmappedVerbs[verbs[i]] > r.UnmappedVerbs[verbs[j]]
			}
			return verbs[i] < verbs[j]
		})
		for _, v := range verbs {
			fmt.Fprintf(&b, "- %s (%d)\n", v, r.UnmappedVerbs[v])
		}
	}
	return b.String()
}
