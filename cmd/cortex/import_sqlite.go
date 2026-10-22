package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/sqliteimport"
)

type importSQLiteOptions struct {
	Path      string
	Force     bool
	EmbedOnly bool
}

func parseImportSQLiteArgs(args []string) (importSQLiteOptions, error) {
	var opts importSQLiteOptions
	for _, a := range args {
		switch a {
		case "--force":
			opts.Force = true
		case "--embed-only":
			opts.EmbedOnly = true
		default:
			if len(a) > 1 && a[0] == '-' {
				return opts, fmt.Errorf("unknown flag: %s", a)
			}
			if opts.Path != "" {
				return opts, fmt.Errorf("unexpected argument: %s", a)
			}
			opts.Path = a
		}
	}
	if opts.Path == "" {
		return opts, fmt.Errorf("usage: cortex import-sqlite <path> [--force] [--embed-only]")
	}
	return opts, nil
}

func cmdImportSQLite() {
	opts, err := parseImportSQLiteArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	cx := openCortex()
	defer cx.Close()
	if err := runImportSQLite(context.Background(), cx, opts, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runImportSQLite imports into the handle's own user (cx.UserID()).
func runImportSQLite(ctx context.Context, cx *cortex.Cortex, opts importSQLiteOptions, out, errw io.Writer) error {
	rep, err := sqliteimport.Import(ctx, opts.Path, cx, cx.UserID(), sqliteimport.Options{
		Force: opts.Force, EmbedOnly: opts.EmbedOnly,
	})
	mismatch := false
	names := make([]string, 0, len(rep.Counts))
	for n := range rep.Counts {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		fmt.Fprintf(out, "%-18s %8s %8s\n", "table", "source", "target")
	}
	for _, n := range names {
		p := rep.Counts[n]
		fmt.Fprintf(out, "%-18s %8d %8d\n", n, p[0], p[1])
		if p[0] != p[1] {
			mismatch = true
		}
	}
	fmt.Fprintf(out, "embedded: %d\n", rep.Embedded)
	if err != nil {
		return err
	}
	if mismatch {
		return fmt.Errorf("source and target counts differ")
	}
	return nil
}
