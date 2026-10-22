package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sausheong/cortex"
)

// listUsers prints one line per user: ID, email (with a verified flag),
// created date and usage.
func listUsers(ctx context.Context, root *cortex.Cortex, w io.Writer) error {
	users, err := root.ListUsers(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%-28s  %-34s  %-10s  %-10s  %s\n", "ID", "EMAIL", "CREATED", "STATUS", "USAGE")
	for _, u := range users {
		email := u.Email
		switch {
		case email == "":
			email = "-"
		case u.EmailVerified:
			email += " (verified)"
		default:
			email += " (unverified)"
		}
		status := "active"
		if u.DeletedAt != nil {
			status = "deleted"
		}
		use := "n/a"
		if us, err := root.ForUser(u.ID).ComputeUsage(ctx); err == nil {
			use = fmt.Sprintf("%d memories, %d entities, %.1f KB", us.Memories, us.Entities, float64(us.Bytes)/1024)
		}
		fmt.Fprintf(w, "%-28s  %-34s  %-10s  %-10s  %s\n", u.ID, email, u.CreatedAt.Format(time.DateOnly), status, use)
	}
	return nil
}

func cmdUsers() {
	if len(os.Args) < 3 || os.Args[2] != "list" {
		fmt.Fprintln(os.Stderr, "usage: cortex users list")
		os.Exit(1)
	}
	root, err := cortex.Open(requireDSN())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer root.Close()
	if err := listUsers(context.Background(), root, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runMigrate opens the database (applying pending migrations) with the given
// timeout and reports the applied versions and elapsed time.
func runMigrate(dsn string, timeout time.Duration, w io.Writer) error {
	start := time.Now()
	root, err := cortex.Open(dsn, cortex.WithMigrationTimeout(timeout))
	if err != nil {
		return err
	}
	defer root.Close()
	vs, err := root.AppliedMigrations(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "migrations applied: %v\n", vs)
	fmt.Fprintf(w, "elapsed: %s\n", time.Since(start).Round(time.Millisecond))
	return nil
}

func cmdMigrate() {
	timeout := 30 * time.Minute
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--timeout" && i+1 < len(args) {
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				fmt.Fprintf(os.Stderr, "invalid --timeout %q\n", args[i+1])
				os.Exit(1)
			}
			timeout = d
			i++
			continue
		}
		fmt.Fprintln(os.Stderr, "usage: cortex migrate [--timeout 30m]")
		os.Exit(1)
	}
	if err := runMigrate(requireDSN(), timeout, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}
