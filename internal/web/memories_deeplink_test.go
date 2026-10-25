package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/model"
)

func TestMemoriesByIDShowsLiveAndRetired(t *testing.T) {
	e, c, _ := memEnv(t)
	ent := &cortex.Entity{Type: "person", Name: "Zxqchipname"}
	if err := c.PutEntity(t.Context(), ent); err != nil {
		t.Fatal(err)
	}
	live := &cortex.Memory{Content: "Zxqlive memory", EntityIDs: []string{ent.ID}, Confidence: 1}
	old := &cortex.Memory{Content: "Zxqretired memory", EntityIDs: []string{ent.ID}, Confidence: 1}
	other := &cortex.Memory{Content: "Zxqother memory", Confidence: 1}
	for _, m := range []*cortex.Memory{live, old, other} {
		if err := c.PutMemory(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.InvalidateMemory(t.Context(), old.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*cortex.Memory{live, old} {
		resp, body := e.get("/memories?id=" + m.ID)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if !strings.Contains(body, m.Content) || strings.Contains(body, "Zxqother") {
			t.Fatalf("id view of %q wrong: %s", m.Content, body)
		}
		if !strings.Contains(body, "Zxqchipname") || !strings.Contains(body, "/memories?entity="+ent.ID) {
			t.Fatalf("entity chip missing for %q", m.Content)
		}
		if !strings.Contains(body, "Show all memories") {
			t.Fatalf("no way back to the full list")
		}
	}
}

func TestMemoriesByIDOtherUserEmpty(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	m := &cortex.Memory{Content: "Zxqalice secret", Confidence: 1}
	if err := e.cx(ua.ID).PutMemory(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	e.newClient()
	e.signIn(Identity{Sub: "g-bob", Email: "bob@x.test", Name: "Bob", EmailVerified: true}, "")
	resp, body := e.get("/memories?id=" + m.ID)
	if resp.StatusCode != 200 || strings.Contains(body, "Zxqalice") || !strings.Contains(body, "Nothing matches") {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
}

func putCorrection(t *testing.T, c *cortex.Cortex, text string) string {
	t.Helper()
	m := &cortex.Memory{Content: text, Speaker: "user", Source: model.SourceVaultCorrection, Confidence: 1}
	if err := c.PutMemory(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func TestCorrectionMemoryShowsVaultLabel(t *testing.T) {
	e, c, _ := memEnv(t)
	id := putCorrection(t, c, "Zxqcorrected fact")
	hand := putMem(t, c, "Zxqhand fact")
	_, body := e.get("/memories?id=" + id)
	if !strings.Contains(body, "you, in Vault") {
		t.Fatalf("label missing: %s", body)
	}
	_, body = e.get("/memories?id=" + hand)
	if strings.Contains(body, "you, in Vault") {
		t.Fatal("label on an ordinary memory")
	}
}

func TestCorrectionNotAttributedToAnyConnectionFilter(t *testing.T) {
	e, c, uid := memEnv(t)
	conn, err := e.st.CreateConnection(t.Context(), uid, "agent", "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	putMem(t, c.WithConnection(conn.ID), "Zxqvia laptop")
	putCorrection(t, c, "Zxqcorrection text")
	_, body := e.get("/memories?by=" + conn.ID)
	if strings.Contains(body, "Zxqcorrection") || !strings.Contains(body, "Zxqvia laptop") {
		t.Fatalf("connection filter wrong: %s", body)
	}
	_, body = e.get("/memories?by=" + model.NoConnection)
	if !strings.Contains(body, "Zxqcorrection") || strings.Contains(body, "Zxqvia laptop") {
		t.Fatalf("no-connection filter wrong: %s", body)
	}
	ms, _, err := c.ListMemories(t.Context(), cortex.MemoryListFilter{WrittenBy: model.NoConnection}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Source == model.SourceVaultCorrection && m.WrittenBy != "" {
			t.Fatalf("WrittenBy set on correction: %q", m.WrittenBy)
		}
	}
}

var inlineHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=|\sstyle\s*=`)

func TestMemoriesPagesCSPClean(t *testing.T) {
	e, c, _ := memEnv(t)
	id := putCorrection(t, c, "Zxqcsp")
	for _, p := range []string{"/memories", "/memories?id=" + id, "/memories?id=missing"} {
		resp, body := e.get(p)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
		if inlineHandler.MatchString(body) {
			t.Errorf("%s: inline style or handler: %s", p, inlineHandler.FindString(body))
		}
	}
}
