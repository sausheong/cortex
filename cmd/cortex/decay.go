package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sausheong/cortex"
)

func cmdDecay() {
	var opts []cortex.DecayOption
	out := ""
	usage := "usage: cortex decay [--half-life <dur>] [--floor <0-1>] [--dry-run] [--out <file>]\n" +
		"  Applies age-based confidence decay to memories and soft-retires any\n" +
		"  whose confidence falls below the floor (reversible; hidden from default\n" +
		"  recall, reachable via include_invalid/as_of). Modifies the graph.\n" +
		"  --half-life  Go duration (default 2160h = 90 days)\n" +
		"  --floor      prune threshold 0-1 (default 0.05)\n" +
		"  --dry-run    report what would change without writing\n" +
		"  --out <file> write the markdown report to a file"
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--half-life":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid --half-life: %v\n", err)
				os.Exit(1)
			}
			opts = append(opts, cortex.WithHalfLife(d))
		case "--floor":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			f, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid --floor: %v\n", err)
				os.Exit(1)
			}
			opts = append(opts, cortex.WithFloor(f))
		case "--dry-run":
			opts = append(opts, cortex.WithDecayDryRun())
		case "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			out = args[i]
		default:
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(1)
		}
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	report, err := cx.DecayConfidence(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decay: %v\n", err)
		os.Exit(1)
	}

	md := cortex.RenderDecayMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote decay report to %s\n", out)
		return
	}
	fmt.Print(md)
}
