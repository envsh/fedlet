package chargectl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindTool(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chargectl-dummy-tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := toolSearchDirs
	toolSearchDirs = []string{dir}
	t.Cleanup(func() { toolSearchDirs = old })

	if got := findTool("chargectl-dummy-tool"); got != bin {
		t.Fatalf("findTool=%q, want %q", got, bin)
	}
	if got := findTool("chargectl-missing-tool"); got != "" {
		t.Fatalf("findTool(missing)=%q, want empty", got)
	}

	// A non-executable file must not be returned.
	noexec := filepath.Join(dir, "chargectl-noexec-tool")
	if err := os.WriteFile(noexec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findTool("chargectl-noexec-tool"); got != "" {
		t.Fatalf("findTool(non-exec)=%q, want empty", got)
	}
}
