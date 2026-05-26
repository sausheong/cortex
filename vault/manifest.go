package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// manifestVersion is bumped if the on-disk format changes incompatibly.
const manifestVersion = 1

// ManifestFilename is the on-disk name of the manifest, stored at the
// vault root.
const ManifestFilename = ".cortex-export.json"

// Manifest tracks which entities have been rendered to which files,
// and the content hash of the rendered page. It enables incremental
// export by allowing the exporter to skip writing pages whose content
// has not changed since the last run.
type Manifest struct {
	Version    int                  `json:"version"`
	ExportedAt time.Time            `json:"exported_at"`
	Pages      map[string]PageEntry `json:"pages"`
}

// PageEntry is a single entity's record in the manifest.
type PageEntry struct {
	Path        string    `json:"path"`         // relative to vault root, forward slashes
	ContentHash string    `json:"content_hash"` // "sha256:" + hex
	ExportedAt  time.Time `json:"exported_at"`
}

func newManifest() *Manifest {
	return &Manifest{
		Version: manifestVersion,
		Pages:   map[string]PageEntry{},
	}
}

// loadManifest reads the manifest at path. A missing file returns an
// empty manifest, not an error — the first export run has no prior state.
// A corrupt or unreadable file returns an error.
func loadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return newManifest(), nil
		}
		return nil, fmt.Errorf("vault: read manifest %s: %w", path, err)
	}
	m := newManifest()
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("vault: parse manifest %s: %w", path, err)
	}
	if m.Pages == nil {
		m.Pages = map[string]PageEntry{}
	}
	return m, nil
}

// saveManifest writes the manifest to path. The caller is responsible
// for ensuring the parent directory exists (the exporter does this once
// per run). The file is written atomically via temp + rename.
func saveManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("vault: marshal manifest: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("vault: write manifest tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("vault: rename manifest: %w", err)
	}
	return nil
}
