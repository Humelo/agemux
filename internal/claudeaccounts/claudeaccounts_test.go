package claudeaccounts

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestDisplayHelpersRespectWideCharacters(t *testing.T) {
	if got := displayWidth("계정 A"); got != 6 {
		t.Fatalf("display width = %d", got)
	}
	clipped := clipDisplay("한국어-account", 9)
	if displayWidth(clipped) > 9 {
		t.Fatalf("clipped text is too wide: %q (%d)", clipped, displayWidth(clipped))
	}
	padded := padDisplay("계정", 8)
	if displayWidth(padded) != 8 {
		t.Fatalf("padded width = %d for %q", displayWidth(padded), padded)
	}
	for _, line := range hardWrap("  ", "한국어-label", 8) {
		if displayWidth(line) > 8 {
			t.Fatalf("wrapped line is too wide: %q (%d)", line, displayWidth(line))
		}
	}
}

func TestNormalizeSearchPreservesUnicode(t *testing.T) {
	if got := normalizeSearch("  한국 계정-2  "); got != "한국 계정 2" {
		t.Fatalf("normalized search = %q", got)
	}
}

func TestInvokedScriptPathPrefersPathEntry(t *testing.T) {
	dir := t.TempDir()
	name := "agemux"
	if os.PathSeparator == '\\' {
		name += ".exe"
	}
	want := filepath.Join(dir, name)
	if err := os.WriteFile(want, []byte("test"), 0755); err != nil {
		t.Fatal(err)
	}
	oldArgs := os.Args
	os.Args = []string{"agemux"}
	t.Cleanup(func() { os.Args = oldArgs })
	t.Setenv("PATH", dir)

	if got := invokedScriptPath(); got != want {
		t.Fatalf("invoked path = %q, want %q", got, want)
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

func TestCommandDeleteRemovesAccountAndPersistsNextCurrent(t *testing.T) {
	restore := useTestClaudeAccountsDataDir(t)
	defer restore()

	first := account{ID: "first", ConfigDir: filepath.Join(home, ".claude-first")}
	second := account{ID: "second", ConfigDir: filepath.Join(home, ".claude-second")}
	if err := saveJSON(accountsFile, accountsDisk{Version: 2, Accounts: []account{first, second}}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := setCurrentAccount(first); err != nil {
		t.Fatal(err)
	}

	if err := commandDelete([]string{"1"}); err != nil {
		t.Fatal(err)
	}

	accounts, err := getAccounts(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || resolvedPath(accounts[0].ConfigDir) != resolvedPath(second.ConfigDir) {
		t.Fatalf("accounts = %#v", accounts)
	}
	if current := accountByID(currentAccountID(accounts), accounts); current == nil || resolvedPath(current.ConfigDir) != resolvedPath(second.ConfigDir) {
		t.Fatalf("current account = %#v", current)
	}
}

func TestCreateNewAccountReservesUniqueDirectories(t *testing.T) {
	restore := useTestClaudeAccountsDataDir(t)
	defer restore()

	accounts := make([]account, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range accounts {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			accounts[index], errs[index] = createNewAccount()
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if resolvedPath(accounts[0].ConfigDir) == resolvedPath(accounts[1].ConfigDir) {
		t.Fatalf("concurrent account creation reused %q", accounts[0].ConfigDir)
	}
	for _, acc := range accounts {
		if st, err := os.Stat(resolvedPath(acc.ConfigDir)); err != nil || !st.IsDir() {
			t.Fatalf("reserved directory %q is missing: %v", acc.ConfigDir, err)
		}
	}
}

func TestCreateNewAccountDoesNotChangeCurrentBeforeLogin(t *testing.T) {
	restore := useTestClaudeAccountsDataDir(t)
	defer restore()

	current := account{ID: "current", ConfigDir: filepath.Join(home, ".claude")}
	if err := setCurrentAccount(current); err != nil {
		t.Fatal(err)
	}
	created, err := createNewAccount()
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == current.ID {
		t.Fatal("new account unexpectedly reused the current account")
	}
	if got := loadState().CurrentID; got != current.ID {
		t.Fatalf("current account changed before login: got %q, want %q", got, current.ID)
	}
}

func useTestClaudeAccountsDataDir(t *testing.T) func() {
	t.Helper()
	oldHome := home
	oldDataDir := dataDir
	oldAccountsFile := accountsFile
	oldAuthFile := authFile
	oldQuotasFile := quotasFile
	oldStateFile := stateFile
	oldLockFile := lockFile

	home = t.TempDir()
	dataDir = t.TempDir()
	accountsFile = filepath.Join(dataDir, "accounts.json")
	authFile = filepath.Join(dataDir, "auth.json")
	quotasFile = filepath.Join(dataDir, "quotas.json")
	stateFile = filepath.Join(dataDir, "state.json")
	lockFile = filepath.Join(dataDir, "claude-accounts.lock")

	return func() {
		home = oldHome
		dataDir = oldDataDir
		accountsFile = oldAccountsFile
		authFile = oldAuthFile
		quotasFile = oldQuotasFile
		stateFile = oldStateFile
		lockFile = oldLockFile
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
