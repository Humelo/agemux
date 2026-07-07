package claudeaccounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseUsageText(t *testing.T) {
	usage := parseUsageText(`Current session: 12.5% used · resets 3pm
Current week (all models): 40% used · resets Monday
Current week (Fable): 1% used`)

	if got := usage["five_hour"]["used_percentage"]; got != 12.5 {
		t.Fatalf("five_hour used = %#v", got)
	}
	if got := usage["seven_day"]["resets_at_display"]; got != "Monday" {
		t.Fatalf("seven_day reset = %#v", got)
	}
	if got := usage["fable_week"]["used_percentage"]; got != 1.0 {
		t.Fatalf("fable_week used = %#v", got)
	}
}

func TestWrapDisplaySplitsLongCells(t *testing.T) {
	lines := wrapDisplay("abcdefghijklmnopqrstuvwxyz", 8, 2)
	if len(lines) != 2 {
		t.Fatalf("expected two lines, got %d", len(lines))
	}
	if lines[0] != "abcdefgh" {
		t.Fatalf("first line = %q", lines[0])
	}
	if lines[1] != "ijklm..." {
		t.Fatalf("second line = %q", lines[1])
	}
}

func TestGeneratedClaudeAccountNamesAreFriendly(t *testing.T) {
	generated := account{ConfigDir: filepath.Join(home, ".claude-"+"sub1"), Aliases: []string{"sub1"}}
	if got := displayAccountHint(generated, nil); !strings.Contains(got, ".claude-"+"sub1") {
		t.Fatalf("generated account label = %q", got)
	}

	custom := account{ConfigDir: filepath.Join(home, ".claude-account-9"), Aliases: []string{"research"}}
	if got := displayAccountHint(custom, nil); got != "research" {
		t.Fatalf("custom alias label = %q", got)
	}
}

func TestClaudePickerRowsIncludeAddAccountAction(t *testing.T) {
	rows := buildPickerRows([]account{{ID: "one", ConfigDir: filepath.Join(home, ".claude")}})
	if len(rows) != 2 {
		t.Fatalf("row count = %d", len(rows))
	}
	if !rows[0].Action || rows[0].Name != "+ Add Claude account" {
		t.Fatalf("first row = %#v", rows[0])
	}
	lines := strings.Join(accountRowLines(rows[0], 100), "\n")
	if !strings.Contains(lines, "Create a new Claude config slot") {
		t.Fatalf("missing add detail: %q", lines)
	}
}

func TestInstallShimMigratesLegacyClswReal(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("shell shim test is POSIX-specific")
	}
	binDir := t.TempDir()
	shimPath := filepath.Join(binDir, "claude")
	legacyRealPath := filepath.Join(binDir, "claude.clsw-real")
	agemuxRealPath := filepath.Join(binDir, "claude.agemux-real")
	if err := os.WriteFile(shimPath, []byte("#!/usr/bin/env bash\n# clsw managed Claude wrapper\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyRealPath, []byte("#!/usr/bin/env bash\necho real\n"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := commandInstallShim([]string{"--bin-dir", binDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agemuxRealPath); err != nil {
		t.Fatalf("agemux real path missing after migration: %v", err)
	}
	if _, err := os.Stat(legacyRealPath); !os.IsNotExist(err) {
		t.Fatalf("legacy real path still exists or stat failed unexpectedly: %v", err)
	}
	content, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "agemux managed Claude wrapper") {
		t.Fatalf("shim was not rewritten as agemux wrapper: %q", string(content))
	}

	if err := commandUninstallShim([]string{"--bin-dir", binDir}); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), "echo real") {
		t.Fatalf("real Claude launcher was not restored: %q", string(restored))
	}
}
