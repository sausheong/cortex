package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
	"github.com/sausheong/cortex/internal/tenancy"
)

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
func (c Ops) NormalizeGraph(ctx context.Context, opts ...model.NormalizeOption) (model.NormalizeReport, error) {
	cfg := &model.NormalizeConfig{}
	for _, o := range opts {
		o(cfg)
	}
	report := model.NormalizeReport{DryRun: cfg.DryRun, UnmappedVerbs: map[string]int{}}

	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("cortex: begin normalize: %w", err)
	}
	defer tx.Rollback()
	batchID, err := NewBatch(ctx, tx, c.UserID, "normalize")
	if err != nil {
		return report, err
	}
	mo := mergeOpts{batchID: batchID, by: "normalize", sameKind: true}

	ownerID, _ := tenancy.New(c.Handle).OwnerEntityID(ctx)
	ownerNames := map[string]bool{}
	if ownerID != "" {
		_, ownerNames = c.owner(ctx, &ResolveContext{})
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
		 FROM entities e WHERE e.user_id = ? AND e.deleted_at IS NULL ORDER BY e.created_at, e.id`, c.UserID)
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
		canonical, known := ontology.CanonicalEntityType(e.typ)
		if !known {
			if label := ontology.NormalizeType(e.typ); label != "" && label != ontology.TypeConcept {
				if e.attrs == nil {
					e.attrs = map[string]any{}
				}
				if _, ok := e.attrs["type_label"]; !ok {
					e.attrs["type_label"] = label
					e.labelled = true
				}
			}
		}
		e.typ = canonical
		k := canonical + "\x00" + strings.ToLower(strings.TrimSpace(e.name))
		if canonical == ontology.TypePerson && ownerLive && ownerNames[normalizeAlias(model.AliasName, e.name)] {
			k = "owner"
		}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], e)
	}

	// A concept group folds into the one specifically-typed group with the
	// same name: concept is the catch-all, so "FileSG" typed concept once and
	// system once is one thing. With several specific groups it stays apart.
	specific := map[string][]string{} // lower name -> non-concept group keys
	for _, k := range keys {
		if typ, name, ok := strings.Cut(k, "\x00"); ok && typ != ontology.TypeConcept {
			specific[name] = append(specific[name], k)
		}
	}
	for _, k := range keys {
		if typ, name, ok := strings.Cut(k, "\x00"); ok && typ == ontology.TypeConcept && len(specific[name]) == 1 {
			target := specific[name][0]
			groups[target] = append(groups[target], groups[k]...)
			groups[k] = nil
		}
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
		// The group's type is its specific one when a concept was folded in.
		for _, e := range g {
			if e.typ != ontology.TypeConcept {
				keep.typ = e.typ
				break
			}
		}
		for _, drop := range g[1:] {
			if _, err := mergeEntitiesTx(ctx, tx, c.UserID, keep.id, drop.id, mo); err != nil {
				return report, fmt.Errorf("cortex: normalize merge %q into %q: %w", drop.name, keep.name, err)
			}
			report.Merged = append(report.Merged, model.MergedEntity{KeepID: keep.id, DropID: drop.id, Name: keep.name, DropName: drop.name, Type: keep.typ})
		}
		if keep.typ != keep.orig {
			report.Retyped = append(report.Retyped, model.RetypedEntity{ID: keep.id, Name: keep.name, From: keep.orig, To: keep.typ})
		}
		if keep.typ == keep.orig && !keep.labelled {
			continue
		}
		if err := recordChanges(ctx, tx, mo, c.UserID, "entities", "update", "{}", "t.id = ? AND t.user_id = ?", keep.id, c.UserID); err != nil {
			return report, err
		}
		// Re-read attributes: the merges above may have added provenance.
		var attrs string
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(attributes, 'null'::jsonb)::text FROM entities WHERE id = ? AND user_id = ?`, keep.id, c.UserID).Scan(&attrs); err != nil {
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
			keep.typ, string(attrsJSON), keep.id, c.UserID); err != nil {
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
		 WHERE r.user_id = ? AND r.deleted_at IS NULL ORDER BY r.created_at, r.id`, c.UserID)
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
		verb, swap, label := ontology.CanonicalRelation(r.typ, r.srcType, r.tgtType)
		src, tgt := r.src, r.tgt
		if swap {
			src, tgt = tgt, src
		}
		if verb == r.typ && !swap {
			continue
		}
		if verb == ontology.RelRelatedTo && label != "" {
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
			c.UserID, src, tgt, verb, r.id).Scan(&twin)
		if err == nil {
			if err := recordChanges(ctx, tx, mo, c.UserID, "relationships", "update", "{}", "t.id = ? AND t.user_id = ?", twin, c.UserID); err != nil {
				return report, err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE relationships SET evidence_count = evidence_count + ? WHERE id = ? AND user_id = ?`,
				r.evidence, twin, c.UserID); err != nil {
				return report, fmt.Errorf("cortex: normalize fold relationship: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE relationships SET deleted_at = now(), deleted_by = 'normalize', delete_batch_id = ? WHERE id = ? AND user_id = ?`,
				batchID, r.id, c.UserID); err != nil {
				return report, fmt.Errorf("cortex: normalize drop folded relationship: %w", err)
			}
			report.RelationshipsFolded++
			continue
		}
		attrsJSON, err := json.Marshal(attrs)
		if err != nil {
			return report, fmt.Errorf("cortex: marshal relationship attributes: %w", err)
		}
		if err := recordChanges(ctx, tx, mo, c.UserID, "relationships", "update", "{}", "t.id = ? AND t.user_id = ?", r.id, c.UserID); err != nil {
			return report, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE relationships SET type = ?, source_id = ?, target_id = ?, attributes = ?::jsonb WHERE id = ? AND user_id = ?`,
			verb, src, tgt, string(attrsJSON), r.id, c.UserID); err != nil {
			return report, fmt.Errorf("cortex: normalize retype relationship: %w", err)
		}
		report.RelationshipsRetyped++
	}

	if len(report.UnmappedVerbs) == 0 {
		report.UnmappedVerbs = nil
	}
	if cfg.DryRun {
		return report, nil // deferred Rollback discards everything
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("cortex: commit normalize: %w", err)
	}
	report.BatchID = batchID
	return report, nil
}

// RenderNormalizeMarkdown renders a NormalizeReport for the CLI.
func RenderNormalizeMarkdown(r model.NormalizeReport) string {
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
