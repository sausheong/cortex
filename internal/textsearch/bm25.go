package textsearch

import "math"

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

type bm25Posting struct {
	doc int
	tf  int
}

// BM25Index ranks chunks with Okapi BM25 (k1 1.2, b 0.75) and OR semantics
// over the query lexemes.
type BM25Index struct {
	ids      []string
	dl       []int
	avgdl    float64
	postings map[string][]bm25Posting
}

// BuildBM25 indexes docs in a single pass.
func BuildBM25(docs []Doc) *BM25Index {
	ix := &BM25Index{
		ids:      make([]string, len(docs)),
		dl:       make([]int, len(docs)),
		postings: map[string][]bm25Posting{},
	}
	var sum int
	for i, d := range docs {
		ix.ids[i] = d.ID
		tf := map[string]int{}
		lex := Lexemes(d.Text)
		for _, l := range lex {
			tf[l]++
		}
		ix.dl[i] = len(lex)
		sum += len(lex)
		for l, c := range tf {
			ix.postings[l] = append(ix.postings[l], bm25Posting{i, c})
		}
	}
	if len(docs) > 0 {
		ix.avgdl = float64(sum) / float64(len(docs))
	}
	return ix
}

// Search returns up to limit hits (limit <= 0 means no limit).
func (ix *BM25Index) Search(query string, limit int, keep func(string) bool) []Hit {
	qs := QueryLexemes(query)
	n := float64(len(ix.ids))
	if len(qs) == 0 || n == 0 {
		return nil
	}
	scores := map[int]float64{}
	for _, l := range qs {
		p := ix.postings[l]
		if len(p) == 0 {
			continue
		}
		df := float64(len(p))
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for _, po := range p {
			tf := float64(po.tf)
			norm := 1.0
			if ix.avgdl > 0 {
				norm = 1 - bm25B + bm25B*float64(ix.dl[po.doc])/ix.avgdl
			}
			scores[po.doc] += idf * tf * (bm25K1 + 1) / (tf + bm25K1*norm)
		}
	}
	hits := make([]Hit, 0, len(scores))
	for di, s := range scores {
		if keep != nil && !keep(ix.ids[di]) {
			continue
		}
		hits = append(hits, Hit{ID: ix.ids[di], Score: s})
	}
	sortHits(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
