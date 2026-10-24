package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// runSetProvider reports why a user's provider settings cannot be changed
// from the command line: they are sealed under the user's key, so they change
// from Settings while signed in. hc is unused and kept for the caller.
func runSetProvider(ctx context.Context, w io.Writer, st *accounts.Store, root *cortex.Cortex, ref string, args []string, stdin io.Reader, env map[string]string, hc *http.Client) error {
	if ref == "" {
		return errors.New("--user <id|verified-email> is required")
	}
	u, err := resolveUser(ctx, root, ref)
	if err != nil {
		return err
	}
	state, err := st.EncState(ctx, u.ID)
	if err != nil {
		return err
	}
	if state == "none" {
		return errors.New("this user has not enrolled; sign in and protect their memory with a passkey first")
	}
	return errors.New("this user is encrypted; change provider settings from Settings while signed in")
}

// enqueueJob records a queued job row; the serving process picks it up.
func enqueueJob(ctx context.Context, db *sql.DB, userID, kind string) error {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO jobs (id, user_id, kind, status) VALUES ($1,$2,$3,'queued')`,
		"j_"+hex.EncodeToString(b), userID, kind)
	return err
}

func cmdSetProvider(ref string, args []string) error {
	dsn := requireDSN()
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
	env := environMap()
	st, err := accountStore(db, env)
	if err != nil {
		return err
	}
	var stdin io.Reader = os.Stdin
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		stdin = nil // interactive terminal: never block waiting for a key
	}
	return runSetProvider(context.Background(), os.Stdout, st, root, ref, args, stdin, env, nil)
}
