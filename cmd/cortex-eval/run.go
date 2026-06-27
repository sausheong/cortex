package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/sausheong/cortex/eval"
)

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return fs
}

// cmdRun scores the eval set under each ablation config and prints a table.
func cmdRun(ctx context.Context) {
	fs := newFlags("run")
	setPath := fs.String("set", "evalset.json", "eval set JSON (from `generate`)")
	limit := fs.Int("limit", 10, "top-k retrieved per query")
	out := fs.String("out", "", "optional JSON results output path")
	fs.Parse(os.Args[2:])

	data, err := os.ReadFile(*setPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read eval set: %v\n", err)
		os.Exit(1)
	}
	var set []eval.QA
	if err := json.Unmarshal(data, &set); err != nil {
		fmt.Fprintf(os.Stderr, "parse eval set: %v\n", err)
		os.Exit(1)
	}
	if len(set) == 0 {
		fmt.Fprintln(os.Stderr, "eval set is empty")
		os.Exit(1)
	}

	cx := openForRun()
	defer cx.Close()

	configs := eval.DefaultConfigs(*limit)
	results := make([]eval.Result, 0, len(configs))
	for _, cfg := range configs {
		fmt.Fprintf(os.Stderr, "running config %q over %d items...\n", cfg.Name, len(set))
		r, err := eval.Run(ctx, cx, set, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "run %q: %v\n", cfg.Name, err)
			os.Exit(1)
		}
		results = append(results, r)
	}

	printTable(results)

	if *out != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write results: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nWrote results to %s\n", *out)
	}
}

func printTable(results []eval.Result) {
	if len(results) == 0 {
		return
	}
	fmt.Printf("\nBenchmark: %d answerable + %d abstain items, top-k retrieval\n\n",
		results[0].N, results[0].NAbstain)
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "config\tR@1\tR@3\tR@5\tR@10\tMRR\tabstain-acc\tfalse-abstain")
	fmt.Fprintln(w, "------\t---\t---\t---\t----\t---\t-----------\t-------------")
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\n",
			r.Config, r.RecallAt1, r.RecallAt3, r.RecallAt5, r.RecallAt10,
			r.MRR, r.AbstentionAcc, r.FalseAbstention)
	}
	w.Flush()
}
