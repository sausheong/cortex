package files

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

// supportedExts maps file extensions to content type strings.
var supportedExts = map[string]string{
	".md":   "markdown",
	".csv":  "csv",
	".tsv":  "tsv",
	".yaml": "yaml",
	".yml":  "yaml",
	".json": "json",
	".txt":  "text",
	".xml":  "xml",
	".toml": "toml",
	".log":  "text",
}

// Option configures a file Connector.
type Option func(*Connector)

// Connector syncs text files from a directory into a Cortex knowledge graph.
type Connector struct {
	dir string
}

// New creates a new file Connector that syncs supported text files from the given directory.
func New(dir string, opts ...Option) *Connector {
	c := &Connector{dir: dir}
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
	return "file:" + c.dir
}

// Sync walks the configured directory for supported text files and ingests any
// files modified since the last sync into the Cortex knowledge graph.
func (c *Connector) Sync(ctx context.Context, cx *cortex.Cortex) error {
	// Load previous sync state.
	var lastSync time.Time
	raw, err := cx.GetSyncState(ctx, c.connectorKey())
	if err != nil {
		return fmt.Errorf("files: get sync state: %w", err)
	}
	if raw != "" {
		var state syncState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return fmt.Errorf("files: parse sync state: %w", err)
		}
		lastSync = state.LastSync
	}

	syncStart := time.Now().UTC()

	// Walk directory for supported text files.
	err = filepath.Walk(c.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(info.Name()))
		contentType, ok := supportedExts[ext]
		if !ok {
			return nil
		}

		// Skip files not modified since last sync.
		if !lastSync.IsZero() && !info.ModTime().After(lastSync) {
			return nil
		}

		// Read file content.
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("files: read %s: %w", path, err)
		}

		// Ingest into Cortex.
		if err := cx.Remember(ctx, string(content),
			cortex.WithSource("file:"+path),
			cortex.WithContentType(contentType),
		); err != nil {
			return fmt.Errorf("files: remember %s: %w", path, err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("files: walk directory: %w", err)
	}

	// Save sync state.
	stateJSON, err := json.Marshal(syncState{LastSync: syncStart})
	if err != nil {
		return fmt.Errorf("files: marshal sync state: %w", err)
	}
	if err := cx.SetSyncState(ctx, c.connectorKey(), string(stateJSON)); err != nil {
		return fmt.Errorf("files: set sync state: %w", err)
	}

	return nil
}

// ContentType returns the content type for a given file extension, or empty
// string if the extension is not supported.
func ContentType(ext string) string {
	return supportedExts[strings.ToLower(ext)]
}

// Supported returns true if the file extension is a supported text format.
func Supported(ext string) bool {
	_, ok := supportedExts[strings.ToLower(ext)]
	return ok
}
