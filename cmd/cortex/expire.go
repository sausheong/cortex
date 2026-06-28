package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sausheong/cortex"
)

func cmdExpire() {
	var opts []cortex.ExpireOption
	out := ""
	usage := "usage: cortex expire [--dry-run] [--out <file>]\n" +
		"  Soft-retires currently-valid memories whose forget_after has passed\n" +
		"  (reversible; hidden from default recall, reachable via include_invalid/\n" +
		"  as_of). Modifies the graph.\n" +
		"  --dry-run    report what would be retired without writing\n" +
		"  --out <file> write the markdown report to a file"
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			opts = append(opts, cortex.WithExpireDryRun())
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

	report, err := cx.ExpireMemories(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "expire: %v\n", err)
		os.Exit(1)
	}

	md := cortex.RenderExpireMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote expire report to %s\n", out)
		return
	}
	fmt.Print(md)
}
