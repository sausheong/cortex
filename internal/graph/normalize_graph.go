package graph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sausheong/cortex/internal/model"
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
	// Retyping rewrites type, name and attribute ciphertext and their blind
	// indexes; that rewrite is not built yet. Fail loudly rather than write
	// anything a server could read.
	return report, errors.New("cortex: graph normalisation is not available for encrypted accounts yet")
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
