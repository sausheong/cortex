package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/jobs"
)

// runNightly is `cortex maintain` with no --user: phase 1 (per-user
// maintenance) then phase 2 (purge) over all users. A phase 1 failure for a
// user is recorded and reported, and makes the run exit non-zero only after
// the purge has run.
func runNightly(ctx context.Context, dsn string, env map[string]string, opts []cortex.MaintainOption, w io.Writer) error {
	root, err := cortex.Open(dsn)
	if err != nil {
		return err
	}
	defer root.Close()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	st, err := accountStore(db, env)
	if err != nil {
		return err
	}
	pool := jobs.NewPool(1, nil).WithDB(db) // only used to queue rows for the serve process
	rep, err := jobs.Nightly(ctx, root, st, jobs.NightlyOptions{Enqueue: pool.Enqueue, MaintainOpts: opts})
	fmt.Fprintf(w, "maintained %d, skipped %d, failed %d; users purged %d\n", rep.Maintained, rep.Skipped, rep.Failed, rep.UsersPurged)
	ids := make([]string, 0, len(rep.Errors))
	for id := range rep.Errors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(w, "  %s: %s\n", id, rep.Errors[id])
	}
	if err != nil {
		return fmt.Errorf("purge: %w", err)
	}
	if rep.Failed > 0 {
		return fmt.Errorf("%d user(s) failed maintenance", rep.Failed)
	}
	return nil
}

func cmdMaintain() {
	var opts []cortex.MaintainOption
	var decayOpts []cortex.DecayOption
	out := ""
	dryRun := false
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
			dryRun = true
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

	if userRef == "" {
		if out != "" || dryRun {
			fmt.Fprintln(os.Stderr, "--dry-run and --out need --user (the nightly pass covers every user)")
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runNightly(ctx, requireDSN(), environMap(), opts, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "maintain: %v\n", err)
			os.Exit(1)
		}
		return
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
