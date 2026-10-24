package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
)

// Large remembers run in the background so the MCP call returns at once
// instead of holding a connection (and the client's patience) through
// extraction and embedding of a multi-megabyte document.
//
// Design: the tool does no work on the document itself. It writes one jobs
// row holding the raw text, source, speaker and the caller's connection id,
// which makes the save durable (queued jobs resume after a restart, and a
// failed run is retried with a backoff, see Pool.retryLater). The worker then
// runs the ordinary Remember through accounts.Store.ResolveUser with that
// connection id, so rows carry the same provenance as a synchronous save.
// Keyword search therefore finds the document when the job finishes rather
// than instantly: storing chunks in the tool call and extracting later would
// need a second code path through Remember, and a retry of the whole job is
// simpler and keeps one writer. One job runs per worker, so a big document
// occupies one worker and other users' jobs run on the others.
//
// Known limit: a retry or a restart that interrupts a run after its chunks
// were stored can store those chunks twice. Duplicates are harmless to
// recall and are what lint and compaction already clean up.

// RememberPayload is the input of a "remember" job.
type RememberPayload struct {
	Content string `json:"content"`
	Source  string `json:"source,omitempty"`
	Speaker string `json:"speaker,omitempty"`
	ConnID  string `json:"conn,omitempty"`
}

// EnqueueRemember queues a background remember for userID.
func EnqueueRemember(ctx context.Context, p *Pool, userID string, rp RememberPayload) (string, error) {
	b, err := json.Marshal(rp)
	if err != nil {
		return "", err
	}
	return p.EnqueueWith(ctx, userID, RememberKind, string(b))
}

// RememberJob is the body of a "remember" job.
func RememberJob(ctx context.Context, root *cortex.Cortex, st *accounts.Store, userID, payload string) error {
	var rp RememberPayload
	if err := json.Unmarshal([]byte(payload), &rp); err != nil || rp.Content == "" {
		return errors.New("jobs: remember job has no content")
	}
	cx, err := st.ResolveUser(ctx, root, userID, rp.ConnID)
	if err != nil {
		return err
	}
	var opts []cortex.RememberOption
	if rp.Source != "" {
		opts = append(opts, cortex.WithSource(rp.Source))
	}
	if rp.Speaker != "" {
		opts = append(opts, cortex.WithSpeaker(rp.Speaker))
	}
	err = cx.Remember(ctx, rp.Content, opts...)
	// Everything is stored; only some vectors (backfilled nightly) or some
	// extraction are missing. Retrying would only duplicate the text.
	var xp *cortex.ExtractPartialError
	if cortex.IsEmbedPartial(err) || errors.As(err, &xp) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("jobs: remember: %w", err)
	}
	return nil
}
