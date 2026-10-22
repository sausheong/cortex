package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// userRef is the --user value (a user ID or a verified email), "" when absent.
var userRef string

// userCommands are the commands that accept --user.
var userCommands = map[string]bool{"maintain": true, "recall": true, "profile": true, "import-sqlite": true, "sync": true, "users": true}

// stripUserFlag removes every `--user <ref>` pair from args (after the
// program name) and returns the last value.
func stripUserFlag(args []string) (ref string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--user" {
			if i+1 >= len(args) || args[i+1] == "" {
				return "", nil, errors.New("--user requires a user ID or verified email")
			}
			ref = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return ref, rest, nil
}

// resolveUser resolves a --user reference to a live user. An ID or a verified
// email only; ambiguity and unverified emails are errors that list IDs.
func resolveUser(ctx context.Context, root *cortex.Cortex, ref string) (*cortex.User, error) {
	u, err := root.ResolveUserRef(ctx, ref)
	if errors.Is(err, cortex.ErrNotFound) {
		return nil, fmt.Errorf("no user matches %q (run `cortex users list`)", ref)
	}
	if err != nil {
		return nil, err
	}
	if u.DeletedAt != nil {
		return nil, fmt.Errorf("user %s is deleted", u.ID)
	}
	return u, nil
}

// accountStore builds the accounts store from the environment.
func accountStore(db *sql.DB, env map[string]string) (*accounts.Store, error) {
	secret := env["CORTEX_SECRET_KEY"]
	if secret == "" {
		return nil, errors.New("CORTEX_SECRET_KEY is required (32 bytes, hex)")
	}
	sealer, err := accounts.NewSealer(secret)
	if err != nil {
		return nil, fmt.Errorf("CORTEX_SECRET_KEY: %w", err)
	}
	return &accounts.Store{DB: db, Sealer: sealer, AllowPrivate: env["CORTEX_ALLOW_PRIVATE_PROVIDERS"] == "1"}, nil
}

// openUserCortex opens the database and returns a handle for the --user
// reference built from that user's own provider settings (never the
// operator's environment keys).
func openUserCortex(ctx context.Context, dsn, ref string, env map[string]string) (*cortex.Cortex, error) {
	root, err := cortex.Open(dsn)
	if err != nil {
		return nil, err
	}
	u, err := resolveUser(ctx, root, ref)
	if err != nil {
		root.Close()
		return nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		root.Close()
		return nil, err
	}
	st, err := accountStore(db, env)
	if err != nil {
		db.Close()
		root.Close()
		return nil, err
	}
	cx, err := st.ResolveUser(ctx, root, u.ID, "")
	if err != nil {
		db.Close()
		root.Close()
		return nil, err
	}
	return cx, nil
}

func openUserCortexOrExit(ref string) *cortex.Cortex {
	env := environMap()
	cx, err := openUserCortex(context.Background(), requireDSN(), ref, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	return cx
}
