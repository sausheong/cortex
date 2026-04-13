package markdown

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sausheong/cortex"
)

// Option configures a markdown Connector.
type Option func(*Connector)

// WithGlob sets the file matching pattern (unused for Walk-based approach,
// but stored for future use).
func WithGlob(pattern string) Option {
	return func(c *Connector) {
		c.glob = pattern
	}
}

// Connector syncs markdown files from a directory into a Cortex knowledge graph.
type Connector struct {
	dir  string
	glob string
}

// New creates a new markdown Connector that syncs .md files from the given directory.
func New(dir string, opts ...Option) *Connector {
	c := &Connector{
		dir:  dir,
		glob: "**/*.md",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// syncState represents the JSON state stored via Cortex sync state.
type syncState struct {
	LastSync time.Time `json:"last_sync"`
}

// connectorKey returns the sync state key for this connector instance.
func (c *Connector) connectorKey() string {
	return "markdown:" + c.dir
}

// Sync walks the configured directory for .md files and ingests any files
// modified since the last sync into the Cortex knowledge graph.
func (c *Connector) Sync(ctx context.Context, cx *cortex.Cortex) error {
	// Load previous sync state.
	var lastSync time.Time
	raw, err := cx.GetSyncState(ctx, c.connectorKey())
	if err != nil {
		return fmt.Errorf("markdown: get sync state: %w", err)
	}
	if raw != "" {
		var state syncState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return fmt.Errorf("markdown: parse sync state: %w", err)
		}
		lastSync = state.LastSync
	}

	syncStart := time.Now().UTC()

	// Walk directory for .md files.
	err = filepath.Walk(c.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}

		// Skip files not modified since last sync.
		if !lastSync.IsZero() && !info.ModTime().After(lastSync) {
			return nil
		}

		// Read file content.
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("markdown: read %s: %w", path, err)
		}

		// Ingest into Cortex.
		if err := cx.Remember(ctx, string(content),
			cortex.WithSource("markdown:"+path),
			cortex.WithContentType("markdown"),
		); err != nil {
			return fmt.Errorf("markdown: remember %s: %w", path, err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("markdown: walk directory: %w", err)
	}

	// Save sync state.
	stateJSON, err := json.Marshal(syncState{LastSync: syncStart})
	if err != nil {
		return fmt.Errorf("markdown: marshal sync state: %w", err)
	}
	if err := cx.SetSyncState(ctx, c.connectorKey(), string(stateJSON)); err != nil {
		return fmt.Errorf("markdown: set sync state: %w", err)
	}

	return nil
}
