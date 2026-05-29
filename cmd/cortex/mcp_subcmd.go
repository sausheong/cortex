package main

import (
	"flag"
	"fmt"
	"os"

	mcpserver "github.com/sausheong/cortex/cmd/cortex/mcp"
)

type mcpConfig struct {
	transport string
	addr      string
	token     string
}

func parseMCPFlags(args []string) (mcpConfig, error) {
	cfg := mcpConfig{
		transport: mcpserver.EnvOr("CORTEX_TRANSPORT", "stdio"),
		addr:      mcpserver.EnvOr("CORTEX_ADDR", "127.0.0.1:8080"),
		token:     os.Getenv("CORTEX_AUTH_TOKEN"),
	}

	fs := flag.NewFlagSet("cortex mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	transport := fs.String("transport", cfg.transport, "transport: stdio | http")
	addr := fs.String("addr", cfg.addr, "host:port for http transport")

	if err := fs.Parse(args); err != nil {
		return mcpConfig{}, err
	}
	cfg.transport = *transport
	cfg.addr = *addr

	switch cfg.transport {
	case "stdio", "http":
	default:
		return mcpConfig{}, fmt.Errorf("invalid --transport %q (must be stdio or http)", cfg.transport)
	}
	return cfg, nil
}

func cmdMCP() {
	cfg, err := parseMCPFlags(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	cx := openCortex()
	defer cx.Close()

	serveErr := mcpserver.Serve(cx, mcpserver.Config{
		Transport: cfg.transport,
		Addr:      cfg.addr,
		Token:     cfg.token,
	})
	if serveErr != nil {
		fmt.Fprintf(os.Stderr, "mcp server error: %v\n", serveErr)
		os.Exit(1)
	}
}
