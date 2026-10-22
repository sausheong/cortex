package anthropic

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

type countRT struct{ n atomic.Int32 }

func (c *countRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}},
		Body: http.NoBody, Request: r}, nil
}

func TestLLMUsesCustomHTTPClient(t *testing.T) {
	rt := &countRT{}
	l := NewLLM("k", WithBaseURL("https://example.invalid"), WithHTTPClient(&http.Client{Transport: rt}))
	_, _ = l.Summarize(context.Background(), []string{"a"})
	if rt.n.Load() == 0 {
		t.Fatal("custom client not used")
	}
}
