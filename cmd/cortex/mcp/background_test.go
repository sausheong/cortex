package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sausheong/cortex"
)

type queuedRemember struct {
	userID, connID, content, source, speaker string
}

func TestRememberLargeContentIsQueuedNotProcessed(t *testing.T) {
	var mu sync.Mutex
	var got []queuedRemember
	e := newMUEnvOpts(t, WithBackgroundRemember(func(_ context.Context, cx *cortex.Cortex, content, source, speaker string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, queuedRemember{cx.UserID(), cx.ConnectionID(), content, source, speaker})
		return nil
	}))
	url, calls := stubProvider(t)
	uid, tok := e.user(t, "alice", url)
	c := e.client(t, tok)

	big := strings.Repeat("Alice works at Acme on the billing team. ", 400) // ~16 KB
	start := time.Now()
	out, isErr := call(t, c, "remember", map[string]any{"content": big, "source": "doc.md", "speaker": "alice"})
	if isErr || !strings.Contains(out, "processing 17 KB in the background") {
		t.Fatalf("out = %q isErr=%v", out, isErr)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("large remember took %v", time.Since(start))
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("provider called %d times before the job ran", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("queued %d", len(got))
	}
	q := got[0]
	if q.userID != uid || q.connID == "" || q.content != big || q.source != "doc.md" || q.speaker != "alice" {
		t.Fatalf("queued = user %q conn %q source %q speaker %q (content match %v)", q.userID, q.connID, q.source, q.speaker, q.content == big)
	}
}

func TestRememberSmallContentStaysSynchronous(t *testing.T) {
	queued := 0
	e := newMUEnvOpts(t, WithBackgroundRemember(func(context.Context, *cortex.Cortex, string, string, string) error {
		queued++
		return nil
	}))
	url, _ := stubProvider(t)
	uid, tok := e.user(t, "bob", url)
	c := e.client(t, tok)

	out, isErr := call(t, c, "remember", map[string]any{"content": "Bob likes tea."})
	if isErr || out != "remembered" {
		t.Fatalf("out = %q isErr=%v", out, isErr)
	}
	if queued != 0 {
		t.Fatal("small content was queued")
	}
	var n int
	if err := e.st.DB.QueryRow(`SELECT count(*) FROM chunks WHERE user_id = $1`, uid).Scan(&n); err != nil || n == 0 {
		t.Fatalf("chunks = %d err=%v, want stored synchronously", n, err)
	}
}
