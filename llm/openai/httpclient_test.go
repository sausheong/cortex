package openai

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type countRT struct{ n atomic.Int32 }

func (c *countRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: http.NoBody, Request: r}, nil
}

func TestLLMUsesCustomHTTPClient(t *testing.T) {
	rt := &countRT{}
	l := NewLLM("k", WithBaseURL("https://example.invalid/v1"), WithHTTPClient(&http.Client{Transport: rt}))
	_, _ = l.Summarize(context.Background(), []string{"a"})
	if rt.n.Load() == 0 {
		t.Fatal("custom client not used")
	}
}

func TestEmbedderUsesCustomHTTPClient(t *testing.T) {
	rt := &countRT{}
	e := NewEmbedder("k", WithEmbedderBaseURL("https://example.invalid/v1"), WithEmbedderHTTPClient(&http.Client{Transport: rt}))
	_, _ = e.Embed(context.Background(), []string{strings.Repeat("a", 3)})
	if rt.n.Load() == 0 {
		t.Fatal("custom client not used")
	}
}
