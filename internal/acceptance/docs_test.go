package acceptance

import (
	"os"
	"path/filepath"
)

// readDoc reads a file from docs/ops at the repository root.
func readDoc(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "ops", name))
	return string(b), err
}
