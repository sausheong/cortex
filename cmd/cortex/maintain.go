package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sausheong/cortex"
)

func cmdMaintain() {
	var opts []cortex.MaintainOption
	var decayOpts []cortex.DecayOption
	out := ""
	usage := "usage: cortex maintain [--dry-run] [--no-reconcile] [--no-relate] [--no-decay] [--half-life <dur>] [--floor <0-1>] [--out <file>]\n" +
		"  Runs the reconsolidation pass: reconcile contradictions, build\n" +
		"  derives/extends edges, decay confidence and soft-retire stale\n" +
		"  memories, expire memories past their forget_after, then refresh\n" +
		"  profiles. Designed to be run periodically (cron/launchd). Modifies the\n" +
		"  graph unless --dry-run. Under --dry-run, relate, expire, and profile\n" +
		"  are skipped (they have no dry-run path).\n" +
		"  --dry-run       preview; write nothing\n" +
		"  --no-reconcile  skip the reconcile pass\n" +
		"  --no-relate     skip the relate pass\n" +
		"  --no-decay      skip the decay pass\n" +
		"  --half-life     decay half-life Go duration (default 2160h = 90 days)\n" +
		"  --floor         decay prune threshold 0-1 (default 0.05)\n" +
		"  --out <file>    write the markdown report to a file"
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			opts = append(opts, cortex.WithMaintainDryRun())
		case "--no-reconcile":
			opts = append(opts, cortex.WithoutReconcile())
		case "--no-relate":
			opts = append(opts, cortex.WithoutRelate())
		case "--no-decay":
			opts = append(opts, cortex.WithoutDecay())
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
			decayOpts = append(decayOpts, cortex.WithHalfLife(d))
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
			decayOpts = append(decayOpts, cortex.WithFloor(f))
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
	if len(decayOpts) > 0 {
		opts = append(opts, cortex.WithMaintainDecayOptions(decayOpts...))
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	report, err := cx.Maintain(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "maintain: %v\n", err)
		os.Exit(1)
	}

	md := cortex.RenderMaintainMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote maintain report to %s\n", out)
		return
	}
	fmt.Print(md)
}
