package main

import (
	"os"
	"testing"
)

func TestParseFlags_Defaults(t *testing.T) {
	cfg, err := parseFlags([]string{"cortex-mcp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.transport != "stdio" {
		t.Errorf("default transport = %q, want %q", cfg.transport, "stdio")
	}
	if cfg.addr != "127.0.0.1:8080" {
		t.Errorf("default addr = %q, want %q", cfg.addr, "127.0.0.1:8080")
	}
}

func TestParseFlags_TransportHTTP(t *testing.T) {
	cfg, err := parseFlags([]string{"cortex-mcp", "--transport", "http"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.transport != "http" {
		t.Errorf("transport = %q, want %q", cfg.transport, "http")
	}
}

func TestParseFlags_AddrOverride(t *testing.T) {
	cfg, err := parseFlags([]string{"cortex-mcp", "--transport", "http", "--addr", "0.0.0.0:9000"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.addr != "0.0.0.0:9000" {
		t.Errorf("addr = %q, want %q", cfg.addr, "0.0.0.0:9000")
	}
}

func TestParseFlags_EnvFallback(t *testing.T) {
	t.Setenv("CORTEX_TRANSPORT", "http")
	t.Setenv("CORTEX_ADDR", ":9999")
	cfg, err := parseFlags([]string{"cortex-mcp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.transport != "http" {
		t.Errorf("transport from env = %q, want %q", cfg.transport, "http")
	}
	if cfg.addr != ":9999" {
		t.Errorf("addr from env = %q, want %q", cfg.addr, ":9999")
	}
}

func TestParseFlags_FlagBeatsEnv(t *testing.T) {
	t.Setenv("CORTEX_TRANSPORT", "http")
	cfg, err := parseFlags([]string{"cortex-mcp", "--transport", "stdio"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.transport != "stdio" {
		t.Errorf("flag should beat env: got %q, want %q", cfg.transport, "stdio")
	}
}

func TestParseFlags_InvalidTransport(t *testing.T) {
	_, err := parseFlags([]string{"cortex-mcp", "--transport", "carrier-pigeon"})
	if err == nil {
		t.Fatal("expected error for invalid transport")
	}
}

func TestParseFlags_UnknownFlag(t *testing.T) {
	_, err := parseFlags([]string{"cortex-mcp", "--what"})
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

func TestParseFlags_TokenFromEnv(t *testing.T) {
	t.Setenv("CORTEX_AUTH_TOKEN", "s3cret")
	cfg, err := parseFlags([]string{"cortex-mcp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.token != "s3cret" {
		t.Errorf("token = %q, want %q", cfg.token, "s3cret")
	}
}

func TestMain(m *testing.M) {
	// Clear any CORTEX_* env vars that might leak from the surrounding shell
	// (don't want them to influence default-value tests).
	os.Unsetenv("CORTEX_TRANSPORT")
	os.Unsetenv("CORTEX_ADDR")
	os.Unsetenv("CORTEX_AUTH_TOKEN")
	os.Exit(m.Run())
}
