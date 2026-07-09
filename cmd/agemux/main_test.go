//go:build !windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTitleParserPreservesRawTitleText(t *testing.T) {
	var got []string
	parser := &titleParser{callback: func(title string) {
		got = append(got, title)
	}}

	parser.feed([]byte("\x1b]0;A+B 100% done\a"))

	if len(got) != 1 {
		t.Fatalf("expected one title update, got %d", len(got))
	}
	if got[0] != "A+B 100% done" {
		t.Fatalf("title was modified: %q", got[0])
	}
}

func TestRunCommandDoesNotWrapEnvWhenNoOverrides(t *testing.T) {
	for _, key := range []string{
		"AGEMUX_ALT_SCREEN",
		"AGEMUX_CLAUDE_BIN",
		"AGEMUX_CLAUDE_DANGEROUS",
		"AGEMUX_CODEX_BIN",
		"AGEMUX_CODEX_DANGEROUS",
		"AGEMUX_DATA_DIR",
		"AGEMUX_PREFIX",
		"AGEMUX_SHPOOL_BIN",
		"CODEX_HOME",
	} {
		old, had := os.LookupEnv(key)
		os.Unsetenv(key)
		t.Cleanup(func() {
			if had {
				os.Setenv(key, old)
			}
		})
	}

	command := runCommand("agemux-test", "codex-resume", "/tmp/project")
	if command == "" {
		t.Fatal("expected command")
	}
	if command[:12] == "/usr/bin/env" {
		t.Fatalf("unexpected env wrapper without overrides: %q", command)
	}
}

func TestRunCommandPreservesCodeHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/tmp/agemux-codex-home")

	command := runCommand("agemux-test", "codex-resume", "/tmp/project")
	if !strings.Contains(command, "CODEX_HOME=/tmp/agemux-codex-home") {
		t.Fatalf("CODEX_HOME was not preserved: %q", command)
	}
}

func TestClaudeAgentArgsUseAccountRunner(t *testing.T) {
	args, err := agentArgs("claude-resume", "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 5 || args[1] != "claude-accounts" || args[2] != "run" || args[3] != "--" {
		t.Fatalf("Claude args do not use account runner: %#v", args)
	}
	if !containsArg(args, "--resume") {
		t.Fatalf("Claude resume flag missing: %#v", args)
	}
}

func TestAgentArgsUseDangerousPermissionsByDefault(t *testing.T) {
	t.Setenv("AGEMUX_CODEX_DANGEROUS", "")
	t.Setenv("AGEMUX_CLAUDE_DANGEROUS", "")

	codexArgs, err := agentArgs("codex-resume", "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if !containsArg(codexArgs, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("Codex dangerous flag missing by default: %#v", codexArgs)
	}

	claudeArgs, err := agentArgs("claude-resume", "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if !containsArg(claudeArgs, "--dangerously-skip-permissions") {
		t.Fatalf("Claude dangerous flag missing by default: %#v", claudeArgs)
	}
}

func TestAgentArgsCanDisableDangerousPermissions(t *testing.T) {
	t.Setenv("AGEMUX_CODEX_DANGEROUS", "0")
	t.Setenv("AGEMUX_CLAUDE_DANGEROUS", "false")

	codexArgs, err := agentArgs("codex-resume", "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if containsArg(codexArgs, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("Codex dangerous flag should be disabled: %#v", codexArgs)
	}

	claudeArgs, err := agentArgs("claude-resume", "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if containsArg(claudeArgs, "--dangerously-skip-permissions") {
		t.Fatalf("Claude dangerous flag should be disabled: %#v", claudeArgs)
	}
}

func TestShpoolSessionsTimesOut(t *testing.T) {
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then sleep 2; exit 0; fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)
	t.Setenv("AGEMUX_SHPOOL_LIST_TIMEOUT", "100ms")

	_, err := shpoolSessions()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestExecAttachRefusesAttachedSessionWithoutForce(t *testing.T) {
	dir := t.TempDir()
	called := filepath.Join(dir, "called")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Attached\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf '%s\\n' \"$*\" > "+shellQuote(called)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)

	err := execAttach("agemux-test", "", false)
	if err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("expected already-attached refusal, got %v", err)
	}
	if _, statErr := os.Stat(called); !os.IsNotExist(statErr) {
		t.Fatalf("shpool attach should not have been called, stat err = %v", statErr)
	}
}

func TestExecAttachOnlyUsesForceWhenExplicit(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf '%s\\n' \"$*\" > "+shellQuote(argsFile)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)

	if err := execAttach("agemux-test", "", false); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), " -f ") || strings.Contains(string(args), "--force") {
		t.Fatalf("non-force attach used force flag: %q", string(args))
	}

	if err := execAttach("agemux-test", "", true); err != nil {
		t.Fatal(err)
	}
	args, err = os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "-f") {
		t.Fatalf("force attach did not pass -f: %q", string(args))
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func fakeShpoolScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shpool")
	content := "#!/usr/bin/env bash\nset -euo pipefail\n" + body
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func withShpoolBin(t *testing.T, path string) {
	t.Helper()
	old := shpoolBin
	shpoolBin = path
	t.Cleanup(func() {
		shpoolBin = old
	})
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func TestCodexAccountsListAndSwitchUseCodeHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)

	alpha := fakeCodexAuth("alpha@example.invalid")
	beta := fakeCodexAuth("beta@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "auth.alpha.json"), []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.beta.json"), []byte(beta), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(beta), 0600); err != nil {
		t.Fatal(err)
	}

	accounts, err := listCodexAccounts(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts len = %d", len(accounts))
	}
	if !accounts[0].Current || accounts[0].Name != "beta" {
		t.Fatalf("expected current beta first, got %#v", accounts[0])
	}
	if accounts[0].Email != "beta@example.invalid" {
		t.Fatalf("email = %q", accounts[0].Email)
	}

	var alphaAccount codexAccount
	for _, acc := range accounts {
		if acc.Name == "alpha" {
			alphaAccount = acc
			break
		}
	}
	if alphaAccount.Name == "" {
		t.Fatal("missing alpha account")
	}
	if err := switchCodexAccount(alphaAccount); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != alpha {
		t.Fatalf("auth.json was not switched: %q", string(current))
	}
	if st, err := os.Stat(filepath.Join(dir, "auth.json")); err != nil {
		t.Fatal(err)
	} else if st.Mode()&0777 != 0600 {
		t.Fatalf("auth.json mode = %o", st.Mode()&0777)
	}
}

func TestCodexAccountsListSkipsBackupAuthFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)

	alpha := fakeCodexAuth("alpha@example.invalid")
	backup := fakeCodexAuth("backup@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "auth.alpha.json"), []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.backup-20260707-131003.json"), []byte(backup), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}

	accounts, err := listCodexAccounts(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Name != "alpha" {
		t.Fatalf("accounts = %#v", accounts)
	}
}

func TestSwitchCodexAccountBacksUpUntrackedActiveAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)

	alpha := fakeCodexAuth("alpha@example.invalid")
	active := fakeCodexAuth("active@example.invalid")
	alphaPath := filepath.Join(dir, "auth.alpha.json")
	if err := os.WriteFile(alphaPath, []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(active), 0600); err != nil {
		t.Fatal(err)
	}

	if err := switchCodexAccount(codexAccount{Name: "alpha", Path: alphaPath}); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "auth.backup-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, files = %#v", len(backups), backups)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != active {
		t.Fatalf("backup content = %q", string(backup))
	}
	current, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != alpha {
		t.Fatalf("auth.json content = %q", string(current))
	}
}

func TestDeleteCurrentCodexAccountSwitchesToRemainingAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)

	alpha := fakeCodexAuth("alpha@example.invalid")
	beta := fakeCodexAuth("beta@example.invalid")
	alphaPath := filepath.Join(dir, "auth.alpha.json")
	betaPath := filepath.Join(dir, "auth.beta.json")
	if err := os.WriteFile(alphaPath, []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(betaPath, []byte(beta), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}

	next, err := deleteCodexAccount(codexAccount{Name: "alpha", Path: alphaPath, Current: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(alphaPath); !os.IsNotExist(err) {
		t.Fatalf("alpha auth still exists or stat failed unexpectedly: %v", err)
	}
	current, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != beta {
		t.Fatalf("active auth was not switched to beta: %q", string(current))
	}
	if next == nil || next.Name != "beta" || !next.Current {
		t.Fatalf("next account = %#v", next)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "auth.backup-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("delete created backup slots: %#v", backups)
	}
}

func TestDeleteLastCurrentCodexAccountRemovesActiveAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)

	alpha := fakeCodexAuth("alpha@example.invalid")
	alphaPath := filepath.Join(dir, "auth.alpha.json")
	if err := os.WriteFile(alphaPath, []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}

	next, err := deleteCodexAccount(codexAccount{Name: "alpha", Path: alphaPath, Current: true})
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("next account = %#v", next)
	}
	if _, err := os.Stat(alphaPath); !os.IsNotExist(err) {
		t.Fatalf("alpha auth still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("active auth still exists or stat failed unexpectedly: %v", err)
	}
}

func TestSaveCodexAccountCreatesSelectableAuthFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)

	auth := fakeCodexAuth("new.user@example.invalid")
	acc, err := saveCodexAccount("new-user", []byte(auth))
	if err != nil {
		t.Fatal(err)
	}
	if acc.Name != "new-user" || acc.Email != "new.user@example.invalid" {
		t.Fatalf("saved account = %#v", acc)
	}
	if filepath.Base(acc.Path) != "auth.new-user.json" {
		t.Fatalf("account path = %q", acc.Path)
	}
	content, err := os.ReadFile(acc.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != auth {
		t.Fatalf("saved auth = %q", string(content))
	}
	if st, err := os.Stat(acc.Path); err != nil {
		t.Fatal(err)
	} else if st.Mode()&0777 != 0600 {
		t.Fatalf("account auth mode = %o", st.Mode()&0777)
	}

	accounts, err := listCodexAccounts(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Name != "new-user" {
		t.Fatalf("accounts = %#v", accounts)
	}
}

func TestCodexAccountNameHelpers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)

	if got := sanitizeCodexAccountName("New User@example.invalid"); got != "new-user@example.invalid" {
		t.Fatalf("sanitized name = %q", got)
	}
	if err := validateCodexAccountName("../bad"); err == nil {
		t.Fatal("expected invalid account name")
	}
	if err := validateCodexAccountName("backup-20260707"); err == nil {
		t.Fatal("expected backup account name to be reserved")
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.tools.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := uniqueCodexAccountName("tools"); got != "tools-2" {
		t.Fatalf("unique name = %q", got)
	}
}

func TestCodexAddAccountRowIsVisible(t *testing.T) {
	lines := strings.Join(renderCodexAddAccountTUILines(false, 100), "\n")
	if !strings.Contains(lines, "+ Add Codex account") {
		t.Fatalf("missing add row: %q", lines)
	}
}

func fakeCodexAuth(email string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `"}`))
	return `{"tokens":{"id_token":"header.` + payload + `.sig"}}`
}

func TestCodexAccountRowsUseCompactFileName(t *testing.T) {
	acc := codexAccount{
		Name:    "alpha",
		Path:    filepath.Join(t.TempDir(), "auth.alpha.json"),
		Email:   "alpha@example.invalid",
		Current: true,
		Updated: "01-02 03:04",
	}
	lines := codexAccountRowLines(acc, 1, 80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "file:auth.alpha.json") {
		t.Fatalf("missing compact file name: %q", joined)
	}
	if strings.Contains(joined, filepath.Dir(acc.Path)) {
		t.Fatalf("row leaked absolute path: %q", joined)
	}
}

func TestCodexUsageParsingAndRows(t *testing.T) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(`{
	  "plan_type": "pro",
	  "rate_limit": {
	    "primary_window": {"limit_window_seconds": 18000, "used_percent": 12},
	    "secondary_window": {"limit_window_seconds": 604800, "used_percent": 34}
	  },
	  "credits": {"unlimited": false, "balance": "42"},
	  "rate_limit_reset_credits": {"available_count": 3}
	}`), &payload); err != nil {
		t.Fatal(err)
	}

	usage := parseCodexUsage(payload, time.Date(2026, 7, 6, 21, 30, 0, 0, time.UTC))
	if usage.Plan != "pro" || usage.Primary != "5h:12%" || usage.Secondary != "7d:34%" || usage.Credits != "42" || usage.Coupons != "3" {
		t.Fatalf("unexpected usage summary: %#v", usage)
	}

	acc := codexAccount{Name: "tools", Path: filepath.Join(t.TempDir(), "auth.tools.json"), Updated: "07-06 21:30", Usage: usage}
	joined := strings.Join(codexAccountRowLines(acc, 1, 120), "\n")
	for _, want := range []string{"plan:pro", "usage:5h:12%/7d:34%", "credits:42", "coupons:3"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("row missing %q: %q", want, joined)
		}
	}
}

func TestCodexAuthAccessToken(t *testing.T) {
	got := codexAuthAccessToken([]byte(`{"tokens":{"access_token":"access-value"}}`))
	if got != "access-value" {
		t.Fatalf("access token = %q", got)
	}
	if got := codexAuthAccessToken([]byte(`{"tokens":{"accessToken":"camel-value"}}`)); got != "camel-value" {
		t.Fatalf("camel access token = %q", got)
	}
}

func TestCodexUsageRejectsNonChatGPTEndpoint(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("AGEMUX_CODEX_USAGE_URL", server.URL)

	dir := t.TempDir()
	path := filepath.Join(dir, "auth.test.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"token-value"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	usage := fetchCodexUsage(server.Client(), codexAccount{Name: "test", Path: path})
	if usage.Error != "bad-url" {
		t.Fatalf("usage error = %#v", usage)
	}
	if hit {
		t.Fatal("fetch sent token to non-ChatGPT endpoint")
	}
}

func TestCodexUsageSkipsExpiredToken(t *testing.T) {
	token := fakeJWT(map[string]any{"exp": float64(time.Now().Add(-time.Hour).Unix())})
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.test.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"`+token+`"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	usage := fetchCodexUsage(&http.Client{}, codexAccount{Name: "test", Path: path})
	if usage.Error != "token-expired" {
		t.Fatalf("usage error = %#v", usage)
	}
}

func fakeJWT(payload map[string]any) string {
	body, _ := json.Marshal(payload)
	return "header." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}
