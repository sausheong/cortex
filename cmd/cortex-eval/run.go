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
	printSweep(set, results)

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
	fmt.Fprintln(w, "config\tR@1\tR@3\tR@5\tR@10\tMRR\tabstain-acc\teasy\thard\tfalse-abstain")
	fmt.Fprintln(w, "------\t---\t---\t---\t----\t---\t-----------\t----\t----\t-------------")
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\t%.3f\n",
			r.Config, r.RecallAt1, r.RecallAt3, r.RecallAt5, r.RecallAt10,
			r.MRR, r.AbstentionAcc, r.AbstainAccEasy, r.AbstainAccHard, r.FalseAbstention)
	}
	w.Flush()
}

// printSweep recomputes abstention quality at each candidate cosine threshold
// directly from the hybrid config's per-item Strengths — independent of the
// compiled-in threshold. This is the calibration view: the knee of the curve
// (high abstain-acc, low false-abstain) picks the shipping threshold.
func printSweep(set []eval.QA, results []eval.Result) {
	var hybrid *eval.Result
	for i := range results {
		if results[i].Config == "hybrid" {
			hybrid = &results[i]
			break
		}
	}
	if hybrid == nil || len(hybrid.Strengths) == 0 {
		fmt.Println("\n(sweep skipped: no 'hybrid' config result with recorded strengths)")
		return
	}
	rows := eval.ThresholdSweep(set, hybrid.Strengths, eval.DefaultThresholds())
	fmt.Println("\nThreshold sweep (hybrid strengths) — higher abstain-acc + lower false-abstain is better;")
	fmt.Print("pick the knee where abstain-acc is high while false-abstain stays low:\n\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "threshold\teasy\thard\tall\tfalse-abstain")
	fmt.Fprintln(w, "---------\t----\t----\t---\t-------------")
	for _, r := range rows {
		fmt.Fprintf(w, "%.2f\t%.3f\t%.3f\t%.3f\t%.3f\n",
			r.Threshold, r.AbstainAccEasy, r.AbstainAccHard, r.AbstainAccAll, r.FalseAbstain)
	}
	w.Flush()
}
