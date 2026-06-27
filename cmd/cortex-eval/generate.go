package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/eval"
)

// cmdGenerate samples N memories from the graph, asks the LLM to write one
// natural question per memory (whose answer is that memory), and emits an
// eval set. It also appends a few abstention negatives — plausible questions
// about facts deliberately NOT grounded in the sampled set — so abstention
// can be scored. Output is JSON: []eval.QA.
func cmdGenerate(ctx context.Context) {
	fs := newFlags("generate")
	n := fs.Int("n", 100, "number of memories to sample")
	nAbstain := fs.Int("abstain", 10, "number of abstention negatives to add")
	out := fs.String("out", "evalset.json", "output JSON path")
	fs.Parse(os.Args[2:])

	db, err := sql.Open("sqlite", dbPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	mems := sampleMemories(ctx, db, *n)
	if len(mems) == 0 {
		fmt.Fprintln(os.Stderr, "no memories found in the graph")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "sampled %d memories; generating questions...\n", len(mems))

	llm := newLLM()
	var set []eval.QA
	for i, m := range mems {
		q, err := genQuestion(ctx, llm, m.content)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [%d/%d] skip (gen error: %v)\n", i+1, len(mems), err)
			continue
		}
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		set = append(set, eval.QA{
			Question:      q,
			TargetID:      m.id,
			TargetContent: m.content,
			Source:        m.source,
		})
		if (i+1)%25 == 0 {
			fmt.Fprintf(os.Stderr, "  generated %d/%d\n", i+1, len(mems))
		}
	}

	// Abstention negatives: questions about made-up facts the graph shouldn't
	// know. Generated once as a batch from the LLM, themed to look plausible
	// but ungrounded.
	if *nAbstain > 0 {
		negs := genAbstainQuestions(ctx, llm, *nAbstain)
		for _, q := range negs {
			set = append(set, eval.QA{Question: q, Abstain: true})
		}
		fmt.Fprintf(os.Stderr, "added %d abstention negatives\n", len(negs))
	}

	data, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %d eval items (%d answerable + abstain) to %s\n", len(set), len(mems), *out)
}

type sampledMem struct {
	id      string
	content string
	source  string
}

// sampleMemories pulls up to n currently-valid memories at random. It tolerates
// older schemas (no invalid_at/expired_at columns) by falling back to a plain
// select. Long memories are skipped (a good QA target is one crisp fact).
func sampleMemories(ctx context.Context, db *sql.DB, n int) []sampledMem {
	queries := []string{
		`SELECT id, content, COALESCE(source,'') FROM memories
		 WHERE (expired_at IS NULL AND invalid_at IS NULL)
		   AND LENGTH(content) BETWEEN 20 AND 400
		 ORDER BY RANDOM() LIMIT ?`,
		`SELECT id, content, COALESCE(source,'') FROM memories
		 WHERE LENGTH(content) BETWEEN 20 AND 400
		 ORDER BY RANDOM() LIMIT ?`,
	}
	for _, q := range queries {
		rows, err := db.QueryContext(ctx, q, n)
		if err != nil {
			continue // try the schema-tolerant fallback
		}
		var out []sampledMem
		for rows.Next() {
			var m sampledMem
			if err := rows.Scan(&m.id, &m.content, &m.source); err != nil {
				rows.Close()
				break
			}
			out = append(out, m)
		}
		rows.Close()
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

const genQuestionPrompt = `You are writing a benchmark question for a personal knowledge graph.
Given a single FACT, write ONE natural question that a person would ask whose
answer is exactly that fact. The question must be answerable from the fact
alone, must NOT quote the fact verbatim, and must NOT include the answer.
Keep it under 20 words. Return ONLY the question text, nothing else.`

// genQuestion asks the LLM for a question via the Summarize method (a
// general-purpose text-in/text-out call available on the cortex.LLM
// interface), prompting it to produce a question for the given fact.
func genQuestion(ctx context.Context, llm cortex.LLM, fact string) (string, error) {
	// Summarize takes []string and returns a string; we use it as a generic
	// completion by prepending the instruction.
	return llm.Summarize(ctx, []string{genQuestionPrompt, "\nFACT: " + fact})
}

func genAbstainQuestions(ctx context.Context, llm cortex.LLM, n int) []string {
	prompt := fmt.Sprintf(`Write %d distinct, natural questions about obscure personal
facts (people's birthdays, specific dollar amounts, random preferences) that a
personal knowledge graph almost certainly does NOT contain. One per line, no
numbering, no preamble. Return ONLY the questions.`, n)
	resp, err := llm.Summarize(ctx, []string{prompt})
	if err != nil {
		return nil
	}
	var qs []string
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "0123456789.-) "))
		if len(line) > 5 {
			qs = append(qs, line)
		}
		if len(qs) >= n {
			break
		}
	}
	return qs
}
