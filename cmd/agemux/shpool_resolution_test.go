//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShpoolResolutionWithMinimalNonInteractivePath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("AGEMUX_SHPOOL_BIN", "")
	root := t.TempDir()
	executable := filepath.Join(root, "shpool")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '{\"sessions\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	resolved := resolveShpoolWithFallbacks([]string{filepath.Join(root, "absent"), executable})
	if resolved != executable {
		t.Fatalf("got %q, want the installed executable", resolved)
	}
	previous := shpoolBin
	shpoolBin = resolved
	t.Cleanup(func() { shpoolBin = previous })
	if rows, err := shpoolSessionsWithTimeout(time.Second); err != nil || len(rows) != 0 {
		t.Fatalf("resolved shpool must execute in the same restricted environment: %v", err)
	}
}

func TestShpoolResolutionPreservesOverridesAndRejectsUnusableFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)
	t.Setenv("AGEMUX_SHPOOL_BIN", "")
	pathExecutable := filepath.Join(root, "shpool")
	if err := os.WriteFile(pathExecutable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := resolveShpoolWithFallbacks([]string{"/fallback/shpool"}); got != pathExecutable {
		t.Fatalf("PATH precedence changed: %s", got)
	}
	t.Setenv("AGEMUX_SHPOOL_BIN", "/explicit/shpool")
	if got := resolveShpoolWithFallbacks([]string{pathExecutable}); got != "/explicit/shpool" {
		t.Fatalf("override changed: %s", got)
	}
	t.Setenv("AGEMUX_SHPOOL_BIN", "")
	t.Setenv("PATH", t.TempDir())
	if err := os.Chmod(pathExecutable, 0600); err != nil {
		t.Fatal(err)
	}
	if got := resolveShpoolWithFallbacks([]string{root, pathExecutable}); got != "shpool" {
		t.Fatalf("non-executable fallback accepted: %s", got)
	}
}
