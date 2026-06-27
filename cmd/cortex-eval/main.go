// Command cortex-eval is a retrieval-quality benchmark for cortex. It has two
// subcommands:
//
//	cortex-eval generate --n 100 --out evalset.json [--abstain 10] [--abstain-hard 10]
//	    Sample N memories from the graph and use the LLM to write a natural
//	    question for each (synthetic ground truth), plus easy (out-of-domain)
//	    and hard (counterfactual, on-topic) abstention negatives. Writes an
//	    eval set as JSON.
//
//	cortex-eval run --set evalset.json [--limit 10] [--out results.json]
//	    Score the eval set under each ablation config and print a comparison
//	    table.
//
// Configuration mirrors the main cortex binary: CORTEX_DB for the database,
// OPENAI_API_KEY / EMBEDDING_* / LLM_* for providers. The embedder MUST match
// the one that produced the stored embeddings (default text-embedding-3-small,
// 1536 dims) or vector recall will be meaningless.
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "generate":
		cmdGenerate(ctx)
	case "run":
		cmdRun(ctx)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  cortex-eval generate --n <count> --out <evalset.json> [--abstain <count>] [--abstain-hard <count>]
  cortex-eval run --set <evalset.json> [--limit <k>] [--out <results.json>]

env: CORTEX_DB (required), OPENAI_API_KEY / EMBEDDING_MODEL / EMBEDDING_DIMS,
     LLM_PROVIDER / LLM_MODEL (generate needs an LLM to write questions)`)
}
