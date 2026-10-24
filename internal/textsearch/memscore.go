package textsearch

import (
	"math"
	"sort"
)

// Doc is one searchable text with its identifier.
type Doc struct{ ID, Text string }

// Hit is a scored result.
type Hit struct {
	ID    string
	Score float64
}

// MemIndex scores memories with the legacy SQL formula: each query token that
// matches contributes ln(1 + N/df); the sum is divided by sqrt(distinct
// lexemes in the document). Ties break by id ascending.
type MemIndex struct {
	n        int
	postings map[string][]int // lexeme -> doc indexes
	ids      []string
	size     []int // distinct lexemes per doc
	lex      []map[string]struct{}
}

// BuildMemIndex indexes docs in a single pass.
func BuildMemIndex(docs []Doc) *MemIndex {
	ix := &MemIndex{
		n:        len(docs),
		postings: map[string][]int{},
		ids:      make([]string, len(docs)),
		size:     make([]int, len(docs)),
		lex:      make([]map[string]struct{}, len(docs)),
	}
	for i, d := range docs {
		set := map[string]struct{}{}
		for _, l := range Lexemes(d.Text) {
			set[l] = struct{}{}
		}
		ix.ids[i] = d.ID
		ix.size[i] = len(set)
		ix.lex[i] = set
		for l := range set {
			ix.postings[l] = append(ix.postings[l], i)
		}
	}
	return ix
}

// Search returns up to limit hits (limit <= 0 means no limit). keep filters
// results only; document frequencies always cover the whole index.
func (ix *MemIndex) Search(query string, limit int, keep func(id string) bool) []Hit {
	toks := queryTokens(query)
	if len(toks) == 0 || ix.n == 0 {
		return nil
	}
	total := float64(max(ix.n, 1))
	scores := map[int]float64{}
	for _, tok := range toks {
		matches := ix.match(tok)
		if len(matches) == 0 {
			continue
		}
		w := math.Log(1 + total/float64(max(len(matches), 1)))
		for _, di := range matches {
			scores[di] += w
		}
	}
	hits := make([]Hit, 0, len(scores))
	for di, s := range scores {
		if keep != nil && !keep(ix.ids[di]) {
			continue
		}
		hits = append(hits, Hit{ID: ix.ids[di], Score: s / math.Sqrt(float64(max(ix.size[di], 1)))})
	}
	sortHits(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// match returns docs containing every lexeme of tok.
func (ix *MemIndex) match(tok []string) []int {
	var cand []int
	for i, l := range tok {
		p := ix.postings[l]
		if len(p) == 0 {
			return nil
		}
		if i == 0 || len(p) < len(cand) {
			cand = p
		}
	}
	var out []int
	for _, di := range cand {
		ok := true
		for _, l := range tok {
			if _, in := ix.lex[di][l]; !in {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, di)
		}
	}
	return out
}

func sortHits(h []Hit) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].Score != h[j].Score {
			return h[i].Score > h[j].Score
		}
		return h[i].ID < h[j].ID
	})
}
