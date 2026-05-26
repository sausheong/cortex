package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed CORTEX.md.template
var cortexSchemaTemplate []byte

func cmdInitSchema() {
	args := os.Args[2:]
	var target string
	force := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force":
			force = true
		default:
			if target != "" {
				fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", args[i])
				os.Exit(1)
			}
			target = args[i]
		}
	}
	if target == "" {
		target = "CORTEX.md"
	}
	// If target is a directory, append CORTEX.md.
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		target = filepath.Join(target, "CORTEX.md")
	}
	if err := writeSchemaTo(target, force); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %s\n", target)
}

// writeSchemaTo writes the embedded CORTEX.md template to path.
// If the file already exists, it returns an error unless force is true.
func writeSchemaTo(path string, force bool) error {
	if !force {
		_, err := os.Stat(path)
		if err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, cortexSchemaTemplate, 0o644)
}
