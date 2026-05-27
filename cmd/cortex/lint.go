package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/sausheong/cortex"
)

type lintOptions struct {
	LowConfidence          bool
	LowConfidenceThreshold float64 // 0 = use cortex default
	OutPath                string  // "" = stdout
}

func parseLintArgs(args []string) (lintOptions, error) {
	var opts lintOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--low-confidence":
			opts.LowConfidence = true
		case "--low-confidence-threshold":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--low-confidence-threshold requires a value")
			}
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				return opts, fmt.Errorf("invalid --low-confidence-threshold: %w", err)
			}
			opts.LowConfidenceThreshold = v
			opts.LowConfidence = true
			i++
		case "--out":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--out requires a path argument")
			}
			opts.OutPath = args[i+1]
			i++
		default:
			return opts, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	return opts, nil
}

func cmdLint() {
	opts, err := parseLintArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: cortex lint [--low-confidence] [--low-confidence-threshold <0-1>] [--out <file>]")
		os.Exit(1)
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	var lintOpts []cortex.LintOption
	if opts.LowConfidenceThreshold > 0 {
		lintOpts = append(lintOpts, cortex.WithLowConfidenceThreshold(opts.LowConfidenceThreshold))
	} else if opts.LowConfidence {
		lintOpts = append(lintOpts, cortex.WithLowConfidence())
	}

	report, err := cx.Lint(ctx, lintOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	markdown := cortex.RenderLintMarkdown(report)

	if opts.OutPath != "" {
		if err := os.WriteFile(opts.OutPath, []byte(markdown), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: write %s: %v\n", opts.OutPath, err)
			os.Exit(1)
		}
		fmt.Printf("Wrote %s\n", opts.OutPath)
	} else {
		fmt.Print(markdown)
	}
}
