package accounts

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const chatOK = `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`

func hostURL(ip string) string {
	if strings.Contains(ip, ":") {
		return "https://[" + ip + "]/v1"
	}
	return "https://" + ip + "/v1"
}

var blockedIPs = []string{"127.0.0.1", "::1", "10.1.2.3", "192.168.0.1", "172.16.0.1", "169.254.169.254",
	"100.64.0.1", "fc00::1", "0.0.0.0", "::ffff:127.0.0.1"}

func TestSafeClientBlocksAddresses(t *testing.T) {
	chat := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatOK)
	}))
	defer chat.Close()
	for _, ip := range blockedIPs {
		// chat URL blocked: the fixed safe client refuses to dial.
		s := Settings{BaseURL: hostURL(ip), APIKey: "k", EmbedModel: "e", EmbedDims: 512}
		err := TestProvider(context.Background(), s, SafeClient(false, ChatBodyCap))
		if err == nil || err.Error() != "provider unreachable" {
			t.Errorf("chat %s: %v", ip, err)
		}
		// embed URL blocked: chat succeeds through a plain client, embed does not connect.
		s = Settings{BaseURL: chat.URL + "/v1", APIKey: "k", EmbedBaseURL: hostURL(ip), EmbedModel: "e", EmbedDims: 512}
		err = TestProvider(context.Background(), s, chat.Client())
		if err == nil || err.Error() != "provider unreachable" {
			t.Errorf("embed %s: %v", ip, err)
		}
	}
}

func TestSafeClientNoRedirects(t *testing.T) {
	var hits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL, http.StatusFound)
	}))
	defer first.Close()
	resp, err := SafeClient(true, 1<<20).Get(first.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits.Load() != 0 {
		t.Fatalf("status %d hits %d", resp.StatusCode, hits.Load())
	}
}

func TestSafeClientCapsBody(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("a", 2<<20)))
		w.(http.Flusher).Flush()
	}))
	defer big.Close()
	resp, err := SafeClient(true, ChatBodyCap).Get(big.URL)
	if err == nil {
		b, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr == nil || len(b) > ChatBodyCap {
			t.Fatalf("2MB read under 1MB cap: %d bytes err=%v", len(b), rerr)
		}
	}
	mid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("a", 2_400_000)))
	}))
	defer mid.Close()
	resp, err = SafeClient(true, EmbedBodyCap).Get(mid.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || len(b) != 2_400_000 {
		t.Fatalf("embed cap: %d %v", len(b), err)
	}
}

func TestSafeClientIgnoresProxyEnv(t *testing.T) {
	var conns atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			c.Close()
		}
	}()
	t.Setenv("HTTPS_PROXY", "http://"+ln.Addr().String())
	t.Setenv("HTTP_PROXY", "http://"+ln.Addr().String())
	t.Setenv("ALL_PROXY", "http://"+ln.Addr().String())
	c := SafeClient(false, 1<<20)
	if tr := c.Transport.(*capTransport).base.(*http.Transport); tr.Proxy != nil {
		t.Fatal("Transport.Proxy must be nil")
	}
	if _, err := c.Get("https://10.0.0.5/"); err == nil {
		t.Fatal("private target reached")
	}
	if conns.Load() != 0 {
		t.Fatalf("proxy received %d connections", conns.Load())
	}
}

func TestProviderErrorTextFixed(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, "secret-upstream-body")
	}))
	defer srv.Close()
	s := Settings{BaseURL: srv.URL + "/v1", APIKey: "k", EmbedModel: "e", EmbedDims: 512}
	err := TestProvider(context.Background(), s, srv.Client())
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	host := strings.TrimPrefix(srv.URL, "https://")
	if strings.Contains(msg, "secret-upstream-body") || strings.Contains(msg, host) || strings.Contains(msg, "127.0.0.1") {
		t.Fatalf("leaked: %q", msg)
	}
	if msg != "unexpected response" {
		t.Fatalf("got %q", msg)
	}

	rej := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"secret-upstream-body"}}`)
	}))
	defer rej.Close()
	s.BaseURL = rej.URL + "/v1"
	err = TestProvider(context.Background(), s, rej.Client())
	if err == nil || err.Error() != "provider rejected the key" {
		t.Fatalf("got %v", err)
	}
}

func TestAllowPrivateLiftsRestrictions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[`+strings.TrimSuffix(strings.Repeat("0.1,", 256), ",")+`]}]}`)
			return
		}
		io.WriteString(w, chatOK)
	}))
	defer srv.Close()
	s := Settings{BaseURL: srv.URL + "/v1", APIKey: "k", EmbedModel: "e", EmbedDims: 256}
	if err := TestProvider(context.Background(), s, SafeClient(true, ChatBodyCap)); err != nil {
		t.Fatalf("allowPrivate: %v", err)
	}
	if err := TestProvider(context.Background(), s, SafeClient(false, ChatBodyCap)); err == nil {
		t.Fatal("private reached without switch")
	}
}

func TestTestProviderChecksDims(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			io.WriteString(w, `{"data":[{"index":0,"embedding":[0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8]}]}`)
			return
		}
		io.WriteString(w, chatOK)
	}))
	defer srv.Close()
	s := Settings{BaseURL: srv.URL + "/v1", APIKey: "k", EmbedModel: "e", EmbedDims: 512}
	if err := TestProvider(context.Background(), s, SafeClient(true, ChatBodyCap)); err == nil {
		t.Fatal("8-float vector accepted for 512 dims")
	}
}

func TestProvidersAnthropicAndHTTPClient(t *testing.T) {
	s := Settings{Provider: ProviderAnthropic, APIKey: "k", ChatModel: "m", EmbedAPIKey: "e", EmbedBaseURL: "https://emb.example.com/v1"}
	llm, emb, ext, err := Providers(&s, &s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%T", llm) != "*anthropic.LLM" || emb.Dimensions() != 512 || ext == nil {
		t.Fatalf("%T %d", llm, emb.Dimensions())
	}
	o := Settings{APIKey: "k"}
	llm, _, _, _ = Providers(&o, &o, nil)
	if fmt.Sprintf("%T", llm) != "*openai.LLM" {
		t.Fatalf("%T", llm)
	}

	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatOK)
	}))
	defer srv.Close()
	c := Settings{BaseURL: srv.URL + "/v1", APIKey: "k"}
	l, _, _, _ := Providers(&c, &c, srv.Client())
	if _, err := l.Summarize(context.Background(), []string{"x"}); err != nil || hits.Load() == 0 {
		t.Fatalf("custom client unused: %v hits=%d", err, hits.Load())
	}
}
