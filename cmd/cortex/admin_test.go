package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/pgtest"
)

type fixedEmb struct{}

func (fixedEmb) Dimensions() int { return 512 }
func (fixedEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 512)
		v[i%512] = 1
		out[i] = v
	}
	return out, nil
}

func adminRoot(t *testing.T) (*cortex.Cortex, string) {
	t.Helper()
	dsn := pgtest.DSN(t)
	root, err := cortex.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dsn
}

func TestStripUserFlag(t *testing.T) {
	ref, rest, err := stripUserFlag([]string{"--rerank", "--user", "u_1", "q"})
	if err != nil || ref != "u_1" || strings.Join(rest, " ") != "--rerank q" {
		t.Fatalf("%q %v %v", ref, rest, err)
	}
	if _, _, err := stripUserFlag([]string{"--user"}); err == nil {
		t.Fatal("dangling --user accepted")
	}
	for _, c := range []string{"maintain", "recall", "profile", "sync"} {
		if !userCommands[c] {
			t.Fatalf("%s must accept --user", c)
		}
	}
	if userCommands["serve"] || userCommands["forget"] {
		t.Fatal("--user accepted on an unsupported command")
	}
}

func TestUsersListCommand(t *testing.T) {
	root, _ := adminRoot(t)
	ctx := t.Context()
	a, err := root.CreateUser(ctx, "sa", "a@x.test", "A", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := root.CreateUser(ctx, "sb", "b@x.test", "B", false)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := listUsers(ctx, root, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{a.ID, b.ID, cortex.BootstrapUserID, "a@x.test (verified)", "b@x.test (unverified)", "memories"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestUserFlagByIDAndVerifiedEmail(t *testing.T) {
	root, _ := adminRoot(t)
	ctx := t.Context()
	u, err := root.CreateUser(ctx, "sa", "ok@x.test", "A", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolveUser(ctx, root, u.ID); err != nil || got.ID != u.ID {
		t.Fatalf("by id: %v %v", got, err)
	}
	if got, err := resolveUser(ctx, root, "ok@x.test"); err != nil || got.ID != u.ID {
		t.Fatalf("by email: %v %v", got, err)
	}
	if _, err := resolveUser(ctx, root, "nobody@x.test"); err == nil || !strings.Contains(err.Error(), "no user matches") {
		t.Fatalf("no match: %v", err)
	}
}

func TestUserFlagAmbiguousRefusesListsIDs(t *testing.T) {
	root, _ := adminRoot(t)
	ctx := t.Context()
	a, _ := root.CreateUser(ctx, "s1", "dup@x.test", "A", true)
	b, _ := root.CreateUser(ctx, "s2", "dup@x.test", "B", true)
	_, err := resolveUser(ctx, root, "dup@x.test")
	if err == nil || !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("ambiguity must list both IDs: %v", err)
	}
}

func TestUserFlagUnverifiedEmailRejected(t *testing.T) {
	root, _ := adminRoot(t)
	ctx := t.Context()
	u, _ := root.CreateUser(ctx, "s1", "unv@x.test", "A", false)
	_, err := resolveUser(ctx, root, "unv@x.test")
	if err == nil || !strings.Contains(err.Error(), u.ID) {
		t.Fatalf("unverified email must be refused with the ID listed: %v", err)
	}
}

func TestUserFlagDeletedAndMissingSecret(t *testing.T) {
	root, dsn := adminRoot(t)
	ctx := t.Context()
	u, _ := root.CreateUser(ctx, "s1", "del@x.test", "A", true)
	if _, err := openUserCortex(ctx, dsn, u.ID, map[string]string{}); err == nil {
		t.Fatalf("an unenrolled user's content must not open without a key: %v", err)
	}
	db, _ := sql.Open("pgx", dsn)
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET deleted_at = now() WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveUser(ctx, root, u.ID); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("deleted user accepted: %v", err)
	}
}

func TestMigrateCommandReportsVersions(t *testing.T) {
	dsn := pgtest.DSN(t)
	var buf bytes.Buffer
	if err := runMigrate(dsn, 2*time.Minute, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "[1 2 3 4 5 6 7 8 9 10 11 12 13]") || !strings.Contains(out, "elapsed:") {
		t.Fatalf("output: %q", out)
	}
}

// TestAdminCommandsRefuseEncryptedUser re-executes the test binary as the CLI
// for every content-reading command, against an encrypted owner and an
// encrypted --user, and expects the fixed message and exit 1.
func TestAdminCommandsRefuseEncryptedUser(t *testing.T) {
	if args := os.Getenv("CORTEX_TEST_CLI_ARGS"); args != "" {
		os.Args = append([]string{"cortex"}, strings.Split(args, "|")...)
		main()
		return
	}
	root, dsn := adminRoot(t)
	ctx := t.Context()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err := root.CreateUser(ctx, "enc1", "enc@x.test", "Enc", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET encryption_state='encrypted' WHERE id IN ($1,$2)`, u.ID, cortex.BootstrapUserID); err != nil {
		t.Fatal(err)
	}
	want := "this user is encrypted; cortex cannot read their content without their key"
	cmds := [][]string{{"recall", "q"}, {"export", "--dry-run"}, {"lint"}, {"maintain", "--user", u.ID}, {"reconcile"},
		{"relate"}, {"decay", "--dry-run"}, {"expire", "--dry-run"}, {"normalize", "--dry-run"}, {"profile"},
		{"entity", "list"}, {"recall", "--user", u.ID, "q"}, {"profile", "--user", u.ID}}
	for _, c := range cmds {
		t.Run(strings.Join(c, "_"), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestAdminCommandsRefuseEncryptedUser$")
			cmd.Env = append(os.Environ(), "CORTEX_TEST_CLI_ARGS="+strings.Join(c, "|"),
				"CORTEX_DATABASE_URL="+dsn, "CORTEX_SECRET_KEY="+strings.Repeat("ab", 32), "OPENAI_API_KEY=")
			out, err := cmd.CombinedOutput()
			ee, ok := err.(*exec.ExitError)
			if !ok || ee.ExitCode() != 1 {
				t.Fatalf("want exit 1, got %v\n%s", err, out)
			}
			if !strings.Contains(string(out), want) {
				t.Fatalf("missing message:\n%s", out)
			}
		})
	}
}

func TestUsersCommandShowsOnlyMetadata(t *testing.T) {
	root, dsn := adminRoot(t)
	ctx := t.Context()
	u, err := root.CreateUser(ctx, "enc2", "enc2@x.test", "Enc Two", true)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE users SET encryption_state='encrypted' WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := listUsers(ctx, root, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, u.ID) || !strings.Contains(out, "enc2@x.test") {
		t.Fatalf("metadata missing:\n%s", out)
	}
	if strings.Contains(out, "this user is encrypted") {
		t.Fatalf("list must not refuse:\n%s", out)
	}
}
