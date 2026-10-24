package cortex

import (
	"context"
	"errors"
	"sync"

	"github.com/sausheong/cortex/internal/ontology"
)

// extractConcurrency bounds how many chunk extractions run at once.
const extractConcurrency = 4

// IsExtractPartial reports whether err only means some sections were not
// analysed.
func IsExtractPartial(err error) bool {
	var p *ExtractPartialError
	return errors.As(err, &p)
}

// extractParts runs the extractor over content. A single part takes the
// plain one-call path. Several parts are extracted concurrently and merged;
// if some fail the merged result of the rest comes back with an
// *ExtractPartialError, and only when every part fails is the error fatal
// (nil extraction).
func (c *Cortex) extractParts(ctx context.Context, content string, parts []string, contentType string) (*Extraction, error) {
	if len(parts) <= 1 {
		return c.h.Cfg.Extractor.Extract(ctx, content, contentType)
	}
	results := make([]*Extraction, len(parts))
	errs := make([]error, len(parts))
	sem := make(chan struct{}, extractConcurrency)
	var wg sync.WaitGroup
	for i, p := range parts {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i], errs[i] = c.h.Cfg.Extractor.Extract(ctx, p, contentType)
		}()
	}
	wg.Wait()

	var ok []*Extraction
	var failed int
	var first error
	for i := range parts {
		if errs[i] != nil || results[i] == nil {
			failed++
			if first == nil {
				first = errs[i]
				if first == nil {
					first = errors.New("extractor returned no result")
				}
			}
			continue
		}
		ok = append(ok, results[i])
	}
	if len(ok) == 0 {
		return nil, first
	}
	merged := mergeExtractions(ok)
	if failed > 0 {
		return merged, &ExtractPartialError{Failed: failed, Total: len(parts), Err: first}
	}
	return merged, nil
}

// mergeExtractions combines per-chunk extractions, in order, into one.
// Entities dedupe by case-insensitive name and type (first spelling wins,
// attributes merge with the first value kept on conflict). Relationship
// endpoints, which are entity names at this stage, are rewritten to the
// canonical merged name so a relationship resolves even when its entities
// were extracted in a different chunk; duplicates by endpoints and type are
// dropped. Memories dedupe by normalised content.
func mergeExtractions(results []*Extraction) *Extraction {
	out := &Extraction{}
	entityIdx := map[string]int{} // name+type key -> index in out.Entities
	canon := map[string]string{}  // normalised name -> canonical name
	for _, r := range results {
		for _, e := range r.Entities {
			nk := ontology.NormKey(e.Name)
			key := nk + "\x00" + ontology.NormKey(e.Type)
			if i, ok := entityIdx[key]; ok {
				dst := &out.Entities[i]
				for k, v := range e.Attributes {
					if dst.Attributes == nil {
						dst.Attributes = map[string]any{}
					}
					if _, has := dst.Attributes[k]; !has {
						dst.Attributes[k] = v
					}
				}
				if e.Confidence > dst.Confidence {
					dst.Confidence = e.Confidence
				}
				continue
			}
			if e.Attributes != nil {
				attrs := make(map[string]any, len(e.Attributes))
				for k, v := range e.Attributes {
					attrs[k] = v
				}
				e.Attributes = attrs
			}
			entityIdx[key] = len(out.Entities)
			out.Entities = append(out.Entities, e)
			if _, ok := canon[nk]; !ok {
				canon[nk] = e.Name
			}
		}
	}

	resolve := func(name string) string {
		if n, ok := canon[ontology.NormKey(name)]; ok {
			return n
		}
		return name
	}
	seenRel := map[string]bool{}
	seenMem := map[string]bool{}
	for _, r := range results {
		for _, rel := range r.Relationships {
			rel.SourceID = resolve(rel.SourceID)
			rel.TargetID = resolve(rel.TargetID)
			key := ontology.NormKey(rel.SourceID) + "\x00" + ontology.NormKey(rel.TargetID) + "\x00" + ontology.NormKey(rel.Type)
			if seenRel[key] {
				continue
			}
			seenRel[key] = true
			out.Relationships = append(out.Relationships, rel)
		}
		for _, m := range r.Memories {
			key := ontology.NormKey(m.Content)
			if seenMem[key] {
				continue
			}
			seenMem[key] = true
			out.Memories = append(out.Memories, m)
		}
	}
	return out
}
