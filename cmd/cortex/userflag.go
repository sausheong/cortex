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
var userCommands = map[string]bool{"maintain": true, "recall": true, "profile": true, "sync": true, "users": true, "normalize": true}

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
	return &accounts.Store{DB: db, AllowPrivate: env["CORTEX_ALLOW_PRIVATE_PROVIDERS"] == "1"}, nil
}

// errEncryptedUser is what admin commands that read content say for an
// encrypted (or migrating) user. cortex holds no key for them on the CLI.
var errEncryptedUser = errors.New("this user is encrypted; cortex cannot read their content without their key")

// refuseEncrypted returns errEncryptedUser when the user's encryption_state is
// not 'none'. An unknown user passes (the caller reports it).
func refuseEncrypted(ctx context.Context, db *sql.DB, userID string) error {
	var state string
	err := db.QueryRowContext(ctx, `SELECT encryption_state FROM users WHERE id = $1`, userID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "none" {
		return errEncryptedUser
	}
	return nil
}

// refuseEncryptedDSN is refuseEncrypted over a short-lived connection.
func refuseEncryptedDSN(ctx context.Context, dsn, userID string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return refuseEncrypted(ctx, db, userID)
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
	if err := refuseEncrypted(ctx, db, u.ID); err != nil {
		db.Close()
		root.Close()
		return nil, err
	}
	st, err := accountStore(db, env)
	if err != nil {
		db.Close()
		root.Close()
		return nil, err
	}
	cx, err := st.ResolveUser(ctx, root, u.ID, "", nil)
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

// openDefaultUserCortex returns the handle for commands run without --user on
// the server: the bootstrap user (the owner) built from that user's own
// provider settings. ok is false when the bootstrap user has no usable
// provider, in which case the caller falls back to the environment-built
// handle.
func openDefaultUserCortex(ctx context.Context, dsn string, env map[string]string) (cx *cortex.Cortex, ok bool, err error) {
	cx, err = openUserCortex(ctx, dsn, cortex.BootstrapUserID, env)
	if errors.Is(err, accounts.ErrNoProvider) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return cx, true, nil
}

// openCortexAsDefaultUser is openCortex, except that with no --user it acts as
// the bootstrap user with that user's providers when they are configured.
func openCortexAsDefaultUser() *cortex.Cortex {
	if userRef == "" {
		cx, ok, err := openDefaultUserCortex(context.Background(), requireDSN(), environMap())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if ok {
			return cx
		}
	}
	return openCortex()
}
