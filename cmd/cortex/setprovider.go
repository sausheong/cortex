package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// readKeys returns the API key and the embed API key. Environment values win;
// otherwise stdin supplies line 1 (API key) and line 2 (embed key). Keys are
// never taken from argv.
func readKeys(stdin io.Reader, env map[string]string) (key, embedKey string, err error) {
	key, embedKey = strings.TrimSpace(env["CORTEX_PROVIDER_API_KEY"]), strings.TrimSpace(env["CORTEX_PROVIDER_EMBED_API_KEY"])
	if key != "" && embedKey != "" {
		return key, embedKey, nil
	}
	if stdin == nil {
		return key, embedKey, nil
	}
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var lines []string
	for sc.Scan() {
		lines = append(lines, strings.TrimSpace(sc.Text()))
	}
	if err := sc.Err(); err != nil {
		return "", "", err
	}
	if key == "" && len(lines) > 0 {
		key = lines[0]
	}
	if embedKey == "" && len(lines) > 1 {
		embedKey = lines[1]
	}
	return key, embedKey, nil
}

// runSetProvider sets one user's provider settings through accounts.Store.
// hc is the HTTP client for the provider test (nil = the hardened default).
func runSetProvider(ctx context.Context, w io.Writer, st *accounts.Store, root *cortex.Cortex, ref string, args []string, stdin io.Reader, env map[string]string, hc *http.Client) error {
	if ref == "" {
		return errors.New("--user <id|verified-email> is required")
	}
	fs := flag.NewFlagSet("cortex users set-provider", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	provider := fs.String("provider", "", "openai-compatible or anthropic")
	baseURL := fs.String("base-url", "", "chat base URL")
	chatModel := fs.String("chat-model", "", "chat model")
	embedModel := fs.String("embed-model", "", "embedding model")
	embedDims := fs.Int("embed-dims", 0, "embedding dimensions")
	embedURL := fs.String("embed-base-url", "", "embedding base URL")
	skipTest := fs.Bool("skip-test", false, "do not test the provider before saving")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("unexpected positional argument (API keys are read from stdin or the environment, never argv)")
	}
	u, err := resolveUser(ctx, root, ref)
	if err != nil {
		return err
	}
	if state, err := st.EncState(ctx, u.ID); err != nil {
		return err
	} else if state != "none" {
		return errors.New("this user is encrypted; change provider settings from Settings while signed in")
	}
	key, embedKey, err := readKeys(stdin, env)
	if err != nil {
		return err
	}

	prev, err := st.GetSettings(ctx, u.ID, nil)
	first := errors.Is(err, accounts.ErrNoSettings)
	if err != nil && !first {
		return err
	}
	var in accounts.Settings
	if !first {
		in = *prev
	}
	if *provider != "" {
		in.Provider = *provider
	}
	if *baseURL != "" {
		in.BaseURL = *baseURL
	}
	if *chatModel != "" {
		in.ChatModel = *chatModel
	}
	if *embedModel != "" {
		in.EmbedModel = *embedModel
	}
	if *embedDims != 0 {
		in.EmbedDims = *embedDims
	}
	if *embedURL != "" {
		in.EmbedBaseURL = *embedURL
	}
	if key != "" {
		in.APIKey = key
	}
	if embedKey != "" {
		in.EmbedAPIKey = embedKey
	}
	in = in.WithDefaults()
	if in.APIKey == "" {
		return errors.New("no API key: pipe it on stdin or set CORTEX_PROVIDER_API_KEY")
	}
	if err := in.Validate(st.AllowPrivate); err != nil {
		return err
	}
	if !*skipTest {
		tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if hc == nil {
			hc = accounts.SafeClient(st.AllowPrivate, accounts.ChatBodyCap)
		}
		if err := accounts.TestProvider(tctx, in, hc); err != nil {
			return fmt.Errorf("provider test failed, settings unchanged: %w", err)
		}
		fmt.Fprintln(w, "provider test passed")
	}
	needsReembed, err := st.PutSettings(ctx, u.ID, in, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "saved provider settings for %s (%s, chat model %q, embed %s/%d)\n", u.ID, in.Provider, in.ChatModel, in.EmbedModel, in.EmbedDims)
	if first {
		if err := enqueueJob(ctx, st.DB, u.ID, "index"); err != nil {
			fmt.Fprintf(w, "warning: could not queue the index job: %v\n", err)
		} else {
			fmt.Fprintln(w, "queued an index job (the running service picks it up)")
		}
	}
	if needsReembed {
		if err := enqueueJob(ctx, st.DB, u.ID, "reembed"); err != nil {
			return fmt.Errorf("settings saved, but the re-embed job could not be queued: %w", err)
		}
		fmt.Fprintln(w, "RE-EMBED NEEDED: the embedder changed and the user has stored embeddings; queued a reembed job (the running service picks it up within 30 seconds)")
	}
	return nil
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
