package claudeaccounts

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Humelo/agemux/internal/termkey"
	"github.com/gofrs/flock"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

const version = "0.1.12"

var (
	home         = homeDir()
	dataDir      = expandPath(envDefaultAny([]string{"AGEMUX_CLAUDE_ACCOUNTS_DATA_DIR", "CLSW_DATA_DIR"}, defaultClaudeAccountsDataDir()))
	accountsFile = filepath.Join(dataDir, "accounts.json")
	authFile     = filepath.Join(dataDir, "auth.json")
	quotasFile   = filepath.Join(dataDir, "quotas.json")
	stateFile    = filepath.Join(dataDir, "state.json")
	lockFile     = filepath.Join(dataDir, "claude-accounts.lock")
	claudeBin    = resolveClaudeBin()
	ansiRE       = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	usageLineRE  = regexp.MustCompile(`^(Current session|Current week \(all models\)|Current week \(Fable\)):\s+([0-9]+(?:\.[0-9]+)?)%\s+used(?:\s+·\s+resets\s+(.+))?$`)
)

type account struct {
	ID        string   `json:"id"`
	ConfigDir string   `json:"config_dir"`
	Aliases   []string `json:"aliases"`
}

type accountsDisk struct {
	Version           int       `json:"version"`
	Accounts          []account `json:"accounts"`
	IgnoredConfigDirs []string  `json:"ignored_config_dirs"`
}

type stateDisk struct {
	Version   int    `json:"version"`
	CurrentID string `json:"current_id"`
	UpdatedAt int64  `json:"updated_at"`
}

type tableRow struct {
	Index    string
	Current  string
	Name     string
	Detail   string
	Login    string
	Session  string
	Week     string
	Fable    string
	Reset    string
	Updated  string
	RawIndex int
	Acc      account
	Action   bool
}

type pickerAction struct {
	Action  string
	Account *account
}

var numberWords = map[string]int{
	"one": 1, "first": 1,
	"two": 2, "second": 2, "too": 2, "to": 2,
	"three": 3, "third": 3,
	"four": 4, "fourth": 4,
	"five": 5, "fifth": 5,
	"six": 6, "sixth": 6,
	"seven": 7, "seventh": 7,
	"eight": 8, "eighth": 8,
	"nine": 9, "ninth": 9,
}

func RunMain(argv []string) error {
	if len(argv) >= 2 && argv[1] == "statusline" {
		return handleStatusline(argv[1:])
	}
	if len(argv) >= 2 && (argv[1] == "version" || argv[1] == "-v" || argv[1] == "--version") {
		fmt.Printf("Claude accounts %s\n", version)
		return nil
	}
	if len(argv) >= 2 && (argv[1] == "-h" || argv[1] == "--help" || argv[1] == "help") {
		printHelp()
		return nil
	}
	if len(argv) == 1 {
		return interactive()
	}

	command := argv[1]
	rest := argv[2:]
	switch command {
	case "list":
		accounts, err := getAccounts(false, false)
		if err != nil {
			return err
		}
		if !contains(rest, "--cached") {
			refreshAll(accounts, true, true, nil)
		}
		return printTable(accounts)
	case "refresh":
		accounts, err := getAccounts(false, false)
		if err != nil {
			return err
		}
		refreshAll(accounts, true, false, nil)
		return printTable(accounts)
	case "change":
		return commandChange(rest)
	case "current":
		return printCurrent()
	case "env":
		return commandEnv(rest)
	case "run":
		return commandRun(rest)
	case "new":
		return commandNew()
	case "login":
		return commandLoginOrStatus("login", rest)
	case "status":
		return commandLoginOrStatus("status", rest)
	case "delete", "remove", "rm":
		return commandDelete(rest)
	case "add":
		return commandAdd(rest)
	case "init":
		accounts, err := getAccounts(false, true)
		if err != nil {
			return err
		}
		refreshAll(accounts, true, true, nil)
		return printTable(accounts)
	case "install-statusline":
		accounts, err := getAccounts(false, false)
		if err != nil {
			return err
		}
		for _, acc := range accounts {
			_ = installStatusline(acc, false)
		}
		return nil
	case "install-shim":
		return commandInstallShim(rest)
	case "uninstall-shim":
		return commandUninstallShim(rest)
	default:
		accounts, err := getAccounts(false, false)
		if err != nil {
			return err
		}
		auth, _ := loadMap(authFile)
		if len(fuzzyMatches(accounts, auth, []string{command})) > 0 {
			return commandRun(append([]string{command}, rest...))
		}
		printHelp()
		return fmt.Errorf("unknown command or account: %s", command)
	}
}

func printHelp() {
	fmt.Print(`Usage:
  agemux claude-accounts                         open the account picker
  agemux claude-accounts list [--cached]         refresh and print all accounts
  agemux claude-accounts refresh                 refresh auth and /usage for all accounts
  agemux claude-accounts change [selector]       set the current account
  agemux claude-accounts current                 print the current account
  agemux claude-accounts env [--config-dir]      print shell env for the current account
  agemux claude-accounts run [selector] [args...] run Claude with selected/current account
  agemux claude-accounts new                     create a new account slot and start login
  agemux claude-accounts login [selector]        log in to an account
  agemux claude-accounts status [selector]       show Claude auth status for an account
  agemux claude-accounts delete <selector>       remove an account from local management
  agemux claude-accounts add <config-dir>        add a Claude config dir
  agemux claude-accounts init                    discover accounts and install statusline hooks
  agemux claude-accounts install-statusline      install statusline hooks for known accounts
  agemux claude-accounts install-shim [--force] [--bin-dir DIR] make bare claude use current Claude account
  agemux claude-accounts uninstall-shim          remove the bare claude wrapper
  agemux claude-accounts version                 print version

Selectors can be a list number, config-dir fragment, email fragment, or fuzzy text.
`)
}

func homeDir() string {
	if value, err := os.UserHomeDir(); err == nil {
		return value
	}
	return "."
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envDefaultAny(keys []string, fallback string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return fallback
}

func defaultClaudeAccountsDataDir() string {
	next := filepath.Join(home, ".local/share/agemux/claude-accounts")
	legacy := filepath.Join(home, ".local/share/clsw")
	if _, err := os.Stat(next); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}
	return next
}

func resolveClaudeBin() string {
	if value := envDefaultAny([]string{"AGEMUX_CLAUDE_BIN", "CLSW_CLAUDE_BIN"}, ""); value != "" {
		return value
	}
	local := filepath.Join(homeDir(), ".local/bin/claude")
	if st, err := os.Stat(local); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
		if real := managedClaudeRealForShim(local); real != "" {
			return real
		}
		return local
	}
	if found, err := exec.LookPath("claude"); err == nil {
		if real := managedClaudeRealForShim(found); real != "" {
			return real
		}
		return found
	}
	return "claude"
}

func expandPath(path string) string {
	path = os.ExpandEnv(path)
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}

func resolvedPath(path string) string {
	abs, err := filepath.Abs(expandPath(path))
	if err != nil {
		return expandPath(path)
	}
	eval, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return eval
	}
	return abs
}

func displayPath(path string) string {
	text := expandPath(path)
	homeClean := filepath.Clean(home)
	clean := filepath.Clean(text)
	if clean == homeClean {
		return "~"
	}
	if rel, err := filepath.Rel(homeClean, clean); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + filepath.ToSlash(rel)
	}
	return text
}

func accountIDForPath(path string) string {
	sum := sha1.Sum([]byte(resolvedPath(path)))
	return hex.EncodeToString(sum[:])[:10]
}

func ensureDataDir() error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	_ = os.Chmod(dataDir, 0700)
	return nil
}

func withLock(fn func() error) error {
	if err := ensureDataDir(); err != nil {
		return err
	}
	lock := flock.New(lockFile)
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	return fn()
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.%d.tmp", filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadJSON(path string, target any) error {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(content, target); err != nil {
		stamp := time.Now().Format("20060102-150405")
		_ = os.Rename(path, filepath.Join(filepath.Dir(path), filepath.Base(path)+".corrupt-"+stamp))
		return err
	}
	return nil
}

func saveJSON(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return atomicWrite(path, content, mode)
}

func loadMap(path string) (map[string]any, error) {
	var result map[string]any
	if err := withLock(func() error {
		if err := loadJSON(path, &result); errors.Is(err, os.ErrNotExist) {
			result = map[string]any{}
			return nil
		} else if err != nil {
			result = map[string]any{}
			return nil
		}
		if result == nil {
			result = map[string]any{}
		}
		return nil
	}); err != nil {
		return map[string]any{}, err
	}
	return result, nil
}

func saveMap(path string, value map[string]any) error {
	return withLock(func() error {
		return saveJSON(path, value, 0600)
	})
}

func looksLikeClaudeConfigDir(path string) bool {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return false
	}
	markers := []string{".claude.json", ".credentials.json", "credentials.json", "settings.json", "history.jsonl", "sessions", "projects"}
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(path, marker)); err == nil {
			return true
		}
	}
	return false
}

func discoverConfigDirs() []string {
	var candidates []string
	candidates = append(candidates, filepath.Join(home, ".claude"))
	for _, pattern := range []string{".claude-*", ".claude_*"} {
		matches, _ := filepath.Glob(filepath.Join(home, pattern))
		sort.Strings(matches)
		candidates = append(candidates, matches...)
	}
	seen := map[string]bool{}
	var result []string
	for _, candidate := range candidates {
		if !looksLikeClaudeConfigDir(candidate) {
			continue
		}
		key := resolvedPath(candidate)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, displayPath(candidate))
	}
	return result
}

func normalizeAccount(acc account) (account, bool) {
	acc.ConfigDir = strings.TrimSpace(acc.ConfigDir)
	if acc.ConfigDir == "" {
		return account{}, false
	}
	acc.ID = accountIDForPath(acc.ConfigDir)
	aliases := []string{}
	seen := map[string]bool{}
	for _, alias := range acc.Aliases {
		alias = strings.TrimSpace(alias)
		if alias != "" && !seen[alias] {
			aliases = append(aliases, alias)
			seen[alias] = true
		}
	}
	acc.Aliases = aliases
	acc.ConfigDir = displayPath(acc.ConfigDir)
	return acc, true
}

func readAccountsUnlocked() (accountsDisk, error) {
	var disk accountsDisk
	if err := loadJSON(accountsFile, &disk); errors.Is(err, os.ErrNotExist) {
		return accountsDisk{Version: 2}, nil
	} else if err != nil {
		return accountsDisk{Version: 2}, nil
	}
	if disk.Version == 0 {
		disk.Version = 2
	}
	return disk, nil
}

func dedupeAccounts(accounts []account) []account {
	seenPath := map[string]bool{}
	seenID := map[string]bool{}
	var result []account
	for _, acc := range accounts {
		norm, ok := normalizeAccount(acc)
		if !ok {
			continue
		}
		pathKey := resolvedPath(norm.ConfigDir)
		if seenPath[pathKey] || seenID[norm.ID] {
			continue
		}
		seenPath[pathKey] = true
		seenID[norm.ID] = true
		result = append(result, norm)
	}
	return result
}

func getAccounts(readonly, install bool) ([]account, error) {
	var accounts []account
	var ignored map[string]bool
	if err := withLock(func() error {
		disk, _ := readAccountsUnlocked()
		ignored = map[string]bool{}
		for _, path := range disk.IgnoredConfigDirs {
			ignored[resolvedPath(path)] = true
		}
		byPath := map[string]account{}
		for _, acc := range disk.Accounts {
			if norm, ok := normalizeAccount(acc); ok {
				byPath[resolvedPath(norm.ConfigDir)] = norm
			}
		}
		for _, dir := range discoverConfigDirs() {
			key := resolvedPath(dir)
			if ignored[key] {
				continue
			}
			if _, exists := byPath[key]; !exists {
				byPath[key] = account{ID: accountIDForPath(dir), ConfigDir: displayPath(dir), Aliases: []string{}}
			}
		}
		for _, acc := range byPath {
			accounts = append(accounts, acc)
		}
		accounts = dedupeAccounts(accounts)
		sort.SliceStable(accounts, func(i, j int) bool {
			leftDefault := resolvedPath(accounts[i].ConfigDir) == resolvedPath(filepath.Join(home, ".claude"))
			rightDefault := resolvedPath(accounts[j].ConfigDir) == resolvedPath(filepath.Join(home, ".claude"))
			if leftDefault != rightDefault {
				return leftDefault
			}
			return accounts[i].ConfigDir < accounts[j].ConfigDir
		})
		if !readonly {
			disk.Version = 2
			disk.Accounts = accounts
			if disk.IgnoredConfigDirs == nil {
				disk.IgnoredConfigDirs = []string{}
			}
			return saveJSON(accountsFile, disk, 0600)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if install {
		for _, acc := range accounts {
			_ = installStatusline(acc, true)
		}
	}
	return accounts, nil
}

func accountByID(id string, accounts []account) *account {
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i]
		}
	}
	return nil
}

func loadState() stateDisk {
	var state stateDisk
	_ = withLock(func() error {
		_ = loadJSON(stateFile, &state)
		return nil
	})
	return state
}

func saveState(state stateDisk) error {
	return withLock(func() error {
		return saveJSON(stateFile, state, 0600)
	})
}

func currentAccountID(accounts []account) string {
	if len(accounts) == 0 {
		return ""
	}
	state := loadState()
	if accountByID(state.CurrentID, accounts) != nil {
		return state.CurrentID
	}
	if envConfig := os.Getenv("CLAUDE_CONFIG_DIR"); envConfig != "" {
		key := resolvedPath(envConfig)
		for _, acc := range accounts {
			if resolvedPath(acc.ConfigDir) == key {
				return acc.ID
			}
		}
	}
	for _, acc := range accounts {
		if resolvedPath(acc.ConfigDir) == resolvedPath(filepath.Join(home, ".claude")) {
			return acc.ID
		}
	}
	return accounts[0].ID
}

func setCurrentAccount(acc account) error {
	return saveState(stateDisk{Version: 1, CurrentID: acc.ID, UpdatedAt: time.Now().Unix()})
}

func removeAccount(acc account) error {
	return withLock(func() error {
		disk, _ := readAccountsUnlocked()
		removed := resolvedPath(acc.ConfigDir)
		var kept []account
		for _, existing := range disk.Accounts {
			if resolvedPath(existing.ConfigDir) != removed {
				kept = append(kept, existing)
			}
		}
		disk.Accounts = dedupeAccounts(kept)
		ignored := map[string]bool{}
		for _, path := range disk.IgnoredConfigDirs {
			ignored[resolvedPath(path)] = true
		}
		ignored[removed] = true
		disk.IgnoredConfigDirs = sortedKeys(ignored)
		if err := saveJSON(accountsFile, disk, 0600); err != nil {
			return err
		}
		for _, path := range []string{authFile, quotasFile} {
			var data map[string]any
			if err := loadJSON(path, &data); err == nil && data != nil {
				delete(data, acc.ID)
				for _, alias := range acc.Aliases {
					delete(data, alias)
				}
				_ = saveJSON(path, data, 0600)
			}
		}
		var state stateDisk
		_ = loadJSON(stateFile, &state)
		if state.CurrentID == acc.ID {
			state.CurrentID = ""
			state.UpdatedAt = time.Now().Unix()
			_ = saveJSON(stateFile, state, 0600)
		}
		return nil
	})
}

func persistResolvedCurrentAccount(accounts []account) error {
	if len(accounts) == 0 {
		return nil
	}
	current := accountByID(currentAccountID(accounts), accounts)
	if current == nil {
		return nil
	}
	return setCurrentAccount(*current)
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, displayPath(key))
	}
	sort.Strings(keys)
	return keys
}

func reserveNewConfigDir() (string, error) {
	for i := 1; i < 1000; i++ {
		candidate := filepath.Join(home, fmt.Sprintf(".claude-account-%d", i))
		if err := os.Mkdir(candidate, 0700); err == nil {
			return displayPath(candidate), nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	for i := 0; i < 1000; i++ {
		candidate := filepath.Join(home, fmt.Sprintf(".claude-account-%d-%d", time.Now().UnixNano(), i))
		if err := os.Mkdir(candidate, 0700); err == nil {
			return displayPath(candidate), nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("could not reserve a Claude account directory")
}

func createNewAccount() (account, error) {
	var acc account
	if err := withLock(func() error {
		configDir, err := reserveNewConfigDir()
		if err != nil {
			return err
		}
		acc = account{ID: accountIDForPath(configDir), ConfigDir: displayPath(configDir), Aliases: []string{}}
		disk, _ := readAccountsUnlocked()
		disk.Accounts = append(disk.Accounts, acc)
		ignored := map[string]bool{}
		for _, path := range disk.IgnoredConfigDirs {
			ignored[resolvedPath(path)] = true
		}
		delete(ignored, resolvedPath(configDir))
		disk.IgnoredConfigDirs = sortedKeys(ignored)
		disk.Accounts = dedupeAccounts(disk.Accounts)
		if err := saveJSON(accountsFile, disk, 0600); err != nil {
			_ = os.Remove(resolvedPath(configDir))
			return err
		}
		return nil
	}); err != nil {
		return account{}, err
	}
	if err := resetAccountSettings(acc); err != nil {
		return account{}, err
	}
	return acc, nil
}

func commandNew() error {
	acc, err := createNewAccount()
	if err != nil {
		return err
	}
	fmt.Printf("created Claude account slot: %s\n", displayPath(acc.ConfigDir))
	fmt.Println("starting Claude login for the new slot...")
	if err := runClaudeSubcommand(acc, []string{"auth", "login"}); err != nil {
		return err
	}
	return setCurrentAccount(acc)
}

func accountEnv(acc account) []string {
	env := os.Environ()
	env = upsertEnv(env, "CLAUDE_CONFIG_DIR", resolvedPath(acc.ConfigDir))
	env = upsertEnv(env, "AGEMUX_CLAUDE_ACCOUNT_ID", acc.ID)
	return env
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func runProbe(acc account, args []string, timeout time.Duration) ([]byte, []byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBin, args...)
	cmd.Env = accountEnv(acc)
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.Bytes(), stderr.Bytes(), code, fmt.Errorf("timed out after %s", timeout)
	}
	return stdout.Bytes(), stderr.Bytes(), code, err
}

func stripANSI(text string) string {
	return ansiRE.ReplaceAllString(text, "")
}

func parseJSONOutput(text string) map[string]any {
	clean := strings.TrimSpace(stripANSI(text))
	if clean == "" {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(clean), &data); err == nil {
		return data
	}
	for index := len(clean) - 1; index >= 0; index-- {
		if clean[index] != '{' {
			continue
		}
		var candidate map[string]any
		dec := json.NewDecoder(strings.NewReader(clean[index:]))
		if err := dec.Decode(&candidate); err == nil {
			return candidate
		}
	}
	return nil
}

func safeTail(text string, limit int) string {
	text = strings.TrimSpace(stripANSI(text))
	if len(text) <= limit {
		return text
	}
	return text[len(text)-limit:]
}

func updateAuth(acc account, row map[string]any) error {
	safe := map[string]any{"updated_at": time.Now().Unix()}
	for _, key := range []string{"loggedIn", "authMethod", "apiProvider", "email", "orgId", "orgName", "subscriptionType", "returncode", "error"} {
		if value, ok := row[key]; ok {
			safe[key] = value
		}
	}
	return withLock(func() error {
		data := map[string]any{}
		_ = loadJSON(authFile, &data)
		for _, alias := range acc.Aliases {
			delete(data, alias)
		}
		data[acc.ID] = safe
		return saveJSON(authFile, data, 0600)
	})
}

func updateQuota(acc account, row map[string]any) error {
	row["updated_at"] = time.Now().Unix()
	return withLock(func() error {
		data := map[string]any{}
		_ = loadJSON(quotasFile, &data)
		for _, alias := range acc.Aliases {
			delete(data, alias)
		}
		data[acc.ID] = row
		return saveJSON(quotasFile, data, 0600)
	})
}

func clearQuota(acc account) {
	_ = withLock(func() error {
		data := map[string]any{}
		if err := loadJSON(quotasFile, &data); err != nil {
			return nil
		}
		delete(data, acc.ID)
		return saveJSON(quotasFile, data, 0600)
	})
}

func quotaError(acc account, message string) {
	_ = updateQuota(acc, map[string]any{"error": safeTail(message, 400), "rate_limits": map[string]any{}})
}

func refreshAuth(acc account, quiet bool) map[string]any {
	stdout, stderr, code, err := runProbe(acc, []string{"auth", "status"}, 10*time.Second)
	if err != nil && len(stdout) == 0 {
		_ = updateAuth(acc, map[string]any{"loggedIn": nil, "error": err.Error()})
		if !quiet {
			fmt.Fprintf(os.Stderr, "Claude accounts: auth status failed for %s: %s\n", displayAccountHint(acc, nil), err)
		}
		return nil
	}
	data := parseJSONOutput(string(stdout))
	if data == nil {
		_ = updateAuth(acc, map[string]any{"loggedIn": nil, "returncode": code, "error": safeTail(string(stderr)+string(stdout), 400)})
		if !quiet {
			fmt.Fprintf(os.Stderr, "Claude accounts: could not parse auth status for %s\n", displayAccountHint(acc, nil))
		}
		return nil
	}
	data["returncode"] = code
	if len(stderr) > 0 {
		data["error"] = safeTail(string(stderr), 400)
	}
	_ = updateAuth(acc, data)
	if value, ok := data["loggedIn"].(bool); ok && !value {
		clearQuota(acc)
	}
	return data
}

func parseUsageText(text string) map[string]map[string]any {
	result := map[string]map[string]any{}
	for _, line := range strings.Split(text, "\n") {
		matches := usageLineRE.FindStringSubmatch(strings.TrimSpace(line))
		if len(matches) == 0 {
			continue
		}
		key := map[string]string{
			"Current session":           "five_hour",
			"Current week (all models)": "seven_day",
			"Current week (Fable)":      "fable_week",
		}[matches[1]]
		used, _ := strconv.ParseFloat(matches[2], 64)
		row := map[string]any{"used_percentage": used}
		if len(matches) > 3 && matches[3] != "" {
			row["resets_at_display"] = strings.TrimSpace(matches[3])
		}
		result[key] = row
	}
	return result
}

func refreshUsage(acc account, quiet bool) map[string]map[string]any {
	stdout, stderr, _, err := runProbe(acc, []string{"-p", "/usage", "--output-format", "json"}, 20*time.Second)
	if err != nil && len(stdout) == 0 {
		quotaError(acc, err.Error())
		if !quiet {
			fmt.Fprintf(os.Stderr, "Claude accounts: /usage failed for %s: %s\n", displayAccountHint(acc, nil), err)
		}
		return nil
	}
	data := parseJSONOutput(string(stdout))
	if data == nil {
		quotaError(acc, safeTail(string(stderr)+string(stdout), 400))
		if !quiet {
			fmt.Fprintf(os.Stderr, "Claude accounts: could not parse /usage for %s\n", displayAccountHint(acc, nil))
		}
		return nil
	}
	result, _ := data["result"].(string)
	usage := parseUsageText(result)
	if len(usage) == 0 {
		quotaError(acc, "no recognizable /usage limits in output")
		return nil
	}
	_ = updateQuota(acc, map[string]any{
		"rate_limits": map[string]any{
			"five_hour":  usage["five_hour"],
			"seven_day":  usage["seven_day"],
			"fable_week": usage["fable_week"],
		},
		"usage_source": "/usage",
		"session_id":   data["session_id"],
		"uuid":         data["uuid"],
	})
	return usage
}

func refreshAccount(acc account, usage, quiet bool) {
	auth := refreshAuth(acc, quiet)
	if usage && auth != nil {
		if loggedIn, _ := auth["loggedIn"].(bool); loggedIn {
			refreshUsage(acc, quiet)
		}
	}
}

func refreshWorkerCount(total int) int {
	if total <= 0 {
		return 0
	}
	if raw := envDefaultAny([]string{"AGEMUX_CLAUDE_REFRESH_JOBS", "CLSW_REFRESH_JOBS"}, ""); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return max(1, min(total, n))
		}
	}
	return min(total, 8)
}

func refreshAll(accounts []account, usage, quiet bool, progress func(string, account, int)) {
	if len(accounts) == 0 {
		return
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := refreshWorkerCount(len(accounts))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				acc := accounts[idx]
				if progress != nil {
					progress("start", acc, idx+1)
				}
				refreshAccount(acc, usage, quiet)
				if progress != nil {
					progress("done", acc, idx+1)
				}
			}
		}()
	}
	for idx := range accounts {
		jobs <- idx
	}
	close(jobs)
	wg.Wait()
}

func lookupRow(cache map[string]any, acc account) map[string]any {
	if row, ok := cache[acc.ID].(map[string]any); ok {
		return row
	}
	for _, alias := range acc.Aliases {
		if row, ok := cache[alias].(map[string]any); ok {
			return row
		}
	}
	return map[string]any{}
}

func displayAccountHint(acc account, authCache map[string]any) string {
	row := map[string]any{}
	if authCache != nil {
		row = lookupRow(authCache, acc)
	}
	if email, _ := row["email"].(string); email != "" {
		return email
	}
	if len(acc.Aliases) > 0 {
		for _, alias := range acc.Aliases {
			if !isGeneratedAlias(alias) {
				return alias
			}
		}
	}
	if acc.ConfigDir != "" {
		return displayPath(acc.ConfigDir)
	}
	return "Claude account"
}

func isGeneratedAlias(alias string) bool {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" || alias == "main" || alias == "default" {
		return true
	}
	if strings.HasPrefix(alias, "sub") {
		_, err := strconv.Atoi(strings.TrimPrefix(alias, "sub"))
		return err == nil
	}
	return false
}

func displayProfileName(configDir string) string {
	resolved := resolvedPath(configDir)
	defaultDir := resolvedPath(filepath.Join(home, ".claude"))
	if resolved == defaultDir {
		return "default"
	}
	base := filepath.Base(resolved)
	for _, prefix := range []string{".claude-" + "sub", ".claude-account-"} {
		if strings.HasPrefix(base, prefix) {
			suffix := strings.TrimPrefix(base, prefix)
			if n, err := strconv.Atoi(suffix); err == nil {
				return fmt.Sprintf("profile %d", n+1)
			}
		}
	}
	return displayPath(configDir)
}

func authLogin(row map[string]any) string {
	if errText, _ := row["error"].(string); errText != "" {
		return "error"
	}
	if value, ok := row["loggedIn"].(bool); ok {
		if value {
			return "yes"
		}
		return "no"
	}
	return "?"
}

func pctValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case json.Number:
		v, err := typed.Float64()
		return v, err == nil
	default:
		return 0, false
	}
}

func fmtPct(value any, compact bool) string {
	used, ok := pctValue(value)
	if !ok {
		return "-"
	}
	if compact {
		return fmt.Sprintf("%.0f%%", used)
	}
	return fmt.Sprintf("%.1f%%", used)
}

func fmtLeft(value any, compact bool) string {
	used, ok := pctValue(value)
	if !ok {
		return "-"
	}
	left := maxFloat(0, 100-used)
	if compact {
		return fmt.Sprintf("%.0f%% left", left)
	}
	return fmt.Sprintf("%.1f%% left", left)
}

func quotaWindow(row map[string]any, name string) map[string]any {
	rate, _ := row["rate_limits"].(map[string]any)
	if rate == nil {
		return map[string]any{}
	}
	if win, ok := rate[name].(map[string]any); ok {
		return win
	}
	return map[string]any{}
}

func fmtAge(epoch any) string {
	var ts int64
	switch typed := epoch.(type) {
	case float64:
		ts = int64(typed)
	case int64:
		ts = typed
	case int:
		ts = int64(typed)
	default:
		return "-"
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func buildRows(accounts []account) []tableRow {
	quotas, _ := loadMap(quotasFile)
	auth, _ := loadMap(authFile)
	current := currentAccountID(accounts)
	rows := make([]tableRow, 0, len(accounts))
	for idx, acc := range accounts {
		authRow := lookupRow(auth, acc)
		quotaRow := lookupRow(quotas, acc)
		five := quotaWindow(quotaRow, "five_hour")
		seven := quotaWindow(quotaRow, "seven_day")
		fable := quotaWindow(quotaRow, "fable_week")
		reset := "-"
		for _, win := range []map[string]any{five, seven, fable} {
			if value, _ := win["resets_at_display"].(string); value != "" {
				reset = value
				break
			}
		}
		currentMarker := " "
		if acc.ID == current {
			currentMarker = "*"
		}
		rows = append(rows, tableRow{
			Index:    strconv.Itoa(idx + 1),
			Current:  currentMarker,
			Name:     displayAccountHint(acc, auth),
			Login:    authLogin(authRow),
			Session:  fmtLeft(five["used_percentage"], true),
			Week:     fmtLeft(seven["used_percentage"], true),
			Fable:    fmtLeft(fable["used_percentage"], true),
			Reset:    reset,
			Updated:  fmtAge(quotaRow["updated_at"]),
			RawIndex: idx,
			Acc:      acc,
		})
	}
	return rows
}

func buildPickerRows(accounts []account) []tableRow {
	rows := []tableRow{{
		Name:     "+ Add Claude account",
		Detail:   "Create a new Claude config slot and start login",
		RawIndex: -1,
		Action:   true,
	}}
	return append(rows, buildRows(accounts)...)
}

func printTable(accounts []account) error {
	rows := buildRows(accounts)
	width, _, _ := term.GetSize(int(os.Stdout.Fd()))
	if width <= 0 {
		width = 120
	}
	printAccountRows(rows, width, -1)
	return nil
}

func printAccountRows(rows []tableRow, width, selected int) {
	inner := max(1, width-2)
	border := "+" + strings.Repeat("-", inner) + "+"
	fmt.Println(border)
	for idx, row := range rows {
		for _, line := range accountRowLines(row, max(1, inner-2)) {
			text := "| " + padDisplay(line, max(1, inner-2)) + " |"
			if idx == selected {
				text = reverse(text)
			}
			fmt.Println(text)
		}
		fmt.Println(border)
	}
}

func accountRowLines(row tableRow, width int) []string {
	if row.Action {
		lines := hardWrap("", row.Name, max(1, width))
		if row.Detail != "" {
			lines = append(lines, wrapParts("    ", []string{row.Detail}, max(1, width))...)
		}
		return lines
	}
	lineWidth := max(1, width)
	first := fmt.Sprintf("%s%s  %s", row.Current, row.Index, row.Name)
	meta := fmt.Sprintf("login:%s", row.Login)
	if row.Updated != "-" {
		meta += "  updated:" + row.Updated
	}
	limitParts := []string{
		"session:" + row.Session,
		"week:" + row.Week,
		"fable:" + row.Fable,
		"reset:" + row.Reset,
	}
	if displayWidth(first+"  "+meta) <= lineWidth {
		first = first + "  " + meta
	} else {
		limitParts = append([]string{meta}, limitParts...)
	}
	lines := hardWrap("", first, lineWidth)
	return append(lines, wrapParts("    ", limitParts, lineWidth)...)
}

func wrapParts(prefix string, parts []string, width int) []string {
	if width <= displayWidth(prefix) {
		width = displayWidth(prefix) + 1
	}
	var lines []string
	current := prefix
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		if displayWidth(prefix+part) > width {
			if strings.TrimSpace(current) != "" {
				lines = append(lines, current)
				current = prefix
			}
			lines = append(lines, hardWrap(prefix, part, width)...)
			continue
		}
		candidate := current
		if strings.TrimSpace(current) == "" {
			candidate += part
		} else {
			candidate += "  " + part
		}
		if displayWidth(candidate) > width && strings.TrimSpace(current) != "" {
			lines = append(lines, current)
			current = prefix + part
			continue
		}
		current = candidate
	}
	if strings.TrimSpace(current) != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		return []string{prefix}
	}
	return lines
}

func hardWrap(prefix, text string, width int) []string {
	prefixWidth := displayWidth(prefix)
	bodyWidth := max(1, width-prefixWidth)
	runes := []rune(text)
	var lines []string
	for displayWidth(string(runes)) > bodyWidth {
		head, tail := splitDisplayRunes(runes, bodyWidth)
		lines = append(lines, prefix+string(head))
		runes = tail
	}
	if len(runes) > 0 {
		lines = append(lines, prefix+string(runes))
	}
	if len(lines) == 0 {
		return []string{prefix}
	}
	return lines
}

func clipDisplay(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(text) <= width {
		return text
	}
	if width <= 3 {
		head, _ := splitDisplayRunes([]rune(text), width)
		return string(head)
	}
	head, _ := splitDisplayRunes([]rune(text), width-3)
	return string(head) + "..."
}

func padDisplay(text string, width int) string {
	used := displayWidth(text)
	if used >= width {
		return text
	}
	return text + strings.Repeat(" ", width-used)
}

func wrapDisplay(text string, width, maxLines int) []string {
	if width <= 0 {
		return []string{""}
	}
	runes := []rune(text)
	var lines []string
	for displayWidth(string(runes)) > width && len(lines) < maxLines-1 {
		head, tail := splitDisplayRunes(runes, width)
		lines = append(lines, string(head))
		runes = tail
	}
	lines = append(lines, clipDisplay(string(runes), width))
	return lines
}

func displayWidth(text string) int {
	return runewidth.StringWidth(text)
}

func splitDisplayRunes(runes []rune, width int) ([]rune, []rune) {
	if len(runes) == 0 || width <= 0 {
		return nil, runes
	}
	used := 0
	cut := 0
	for cut < len(runes) {
		next := runewidth.RuneWidth(runes[cut])
		if cut > 0 && used+next > width {
			break
		}
		used += next
		cut++
		if used >= width {
			for cut < len(runes) && runewidth.RuneWidth(runes[cut]) == 0 {
				cut++
			}
			break
		}
	}
	return runes[:cut], runes[cut:]
}

func interactive() error {
	accounts, err := getAccounts(false, false)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		fmt.Println("no Claude account configured; creating a new account slot")
		return commandNew()
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) || os.Getenv("TERM") == "dumb" {
		refreshAll(accounts, true, true, nil)
		return printTable(accounts)
	}
	runLoadingRefresh(accounts)
	action, err := picker(accounts, "manage")
	if err != nil {
		return err
	}
	if action.Action == "" || action.Account == nil && action.Action != "new" {
		return nil
	}
	switch action.Action {
	case "change":
		if err := setCurrentAccount(*action.Account); err != nil {
			return err
		}
		fmt.Printf("current Claude account: %s\n", displayAccountHint(*action.Account, nil))
	case "new":
		return commandNew()
	case "login":
		return runClaudeSubcommand(*action.Account, []string{"auth", "login"})
	case "status":
		return runClaudeSubcommand(*action.Account, []string{"auth", "status"})
	}
	return nil
}

func runLoadingRefresh(accounts []account) {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		refreshAll(accounts, true, true, nil)
		return
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)
	fmt.Print("\033[?25l\033[?1049h")
	defer fmt.Print("\033[?1049l\033[?25h")
	done := make(chan struct{})
	var mu sync.Mutex
	active := map[int]string{}
	go func() {
		refreshAll(accounts, true, true, func(event string, acc account, idx int) {
			mu.Lock()
			defer mu.Unlock()
			if event == "start" {
				active[idx] = displayAccountHint(acc, nil)
			} else {
				delete(active, idx)
			}
		})
		close(done)
	}()
	spinner := []string{"|", "/", "-", "\\"}
	frame := 0
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			width, height, _ := term.GetSize(int(os.Stdout.Fd()))
			if width <= 0 {
				width = 80
			}
			if height <= 0 {
				height = 24
			}
			fmt.Print("\033[H\033[2J")
			tuiLine(bold("Claude accounts") + " - refreshing " + spinner[frame%len(spinner)])
			tuiLine(dim("auth status and /usage refresh run in parallel"))
			tuiLine("")
			mu.Lock()
			var lines []string
			for idx, path := range active {
				lines = append(lines, fmt.Sprintf("%d  %s", idx, path))
			}
			mu.Unlock()
			sort.Strings(lines)
			if len(lines) == 0 {
				lines = append(lines, "waiting for workers...")
			}
			for _, line := range lines[:min(len(lines), max(0, height-5))] {
				tuiLine(clipDisplay(line, width-1))
			}
			frame++
		}
	}
}

func picker(accounts []account, mode string) (pickerAction, error) {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return pickerAction{}, err
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)
	fmt.Print("\033[?25l\033[?1049h")
	defer fmt.Print("\033[?1049l\033[?25h")

	selected := 0
	current := currentAccountID(accounts)
	for i, acc := range accounts {
		if acc.ID == current {
			selected = i + 1
			break
		}
	}
	buf := make([]byte, 16)
	for {
		rows := buildPickerRows(accounts)
		drawPicker(rows, selected, mode, len(accounts))
		key, err := termkey.Read(os.Stdin, buf)
		if err != nil {
			return pickerAction{}, err
		}
		rowCount := len(accounts) + 1
		switch {
		case key == "\x1b[A":
			selected = (selected - 1 + rowCount) % rowCount
		case key == "\x1b[B" || key == "j":
			selected = (selected + 1) % rowCount
		case key == "\r" || key == "\n":
			if selected == 0 {
				return pickerAction{Action: "new"}, nil
			}
			return pickerAction{Action: "change", Account: &accounts[selected-1]}, nil
		case key == "n":
			return pickerAction{Action: "new"}, nil
		case key == "l":
			if selected > 0 {
				return pickerAction{Action: "login", Account: &accounts[selected-1]}, nil
			}
		case key == "s":
			if selected > 0 {
				return pickerAction{Action: "status", Account: &accounts[selected-1]}, nil
			}
		case key == "r":
			refreshAll(accounts, true, true, nil)
		case key == "d" || key == "k" || key == "x":
			if selected == 0 {
				continue
			}
			accountIndex := selected - 1
			if confirm(fmt.Sprintf("Delete %s from Claude accounts? y/N", displayAccountHint(accounts[accountIndex], nil))) {
				if err := removeAccount(accounts[accountIndex]); err != nil {
					return pickerAction{}, err
				}
				var reloadErr error
				accounts, reloadErr = getAccounts(false, false)
				if reloadErr != nil {
					return pickerAction{}, reloadErr
				}
				if err := persistResolvedCurrentAccount(accounts); err != nil {
					return pickerAction{}, err
				}
				if len(accounts) == 0 {
					return pickerAction{}, nil
				}
				if selected > len(accounts) {
					selected = len(accounts)
				}
			}
		case key == "q" || key == "\x1b":
			return pickerAction{}, nil
		}
	}
}

func drawPicker(rows []tableRow, selected int, mode string, accountCount int) {
	width, height, _ := term.GetSize(int(os.Stdout.Fd()))
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 24
	}
	fmt.Print("\033[H\033[2J")
	tuiLine(bold(clipDisplay("Claude accounts", width-1)))
	tuiLine(dim(clipDisplay("Up/Down move  Enter select  n new  l login  s status  r refresh  d delete  q/Esc back", width-1)))
	tuiLine(strings.Repeat("-", min(width-1, 1000)))
	visible := max(1, (height-5)/4)
	offset := 0
	if selected >= visible {
		offset = selected - visible + 1
	}
	for i, row := range rows[offset:min(len(rows), offset+visible)] {
		idx := offset + i
		for _, line := range renderAccountTUILines(row, idx == selected, width-1) {
			tuiLine(line)
		}
		tuiLine("")
	}
	fmt.Printf("\033[%d;1H%s", height, dim(fmt.Sprintf("%d account(s)", accountCount)))
}

func renderAccountTUILines(row tableRow, selected bool, width int) []string {
	lines := accountRowLines(row, max(1, width-2))
	rendered := make([]string, 0, len(lines))
	for i, line := range lines {
		prefix := "  "
		if selected && i == 0 {
			prefix = "> "
		}
		text := padDisplay(prefix+line, width)
		if selected {
			text = reverse(text)
		} else if i > 0 {
			text = dim(text)
		}
		rendered = append(rendered, text)
	}
	return rendered
}

func tuiLine(s string) {
	fmt.Print(s + "\r\n")
}

func confirm(prompt string) bool {
	width, height, _ := term.GetSize(int(os.Stdout.Fd()))
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	fmt.Printf("\033[%d;1H%s", height, reverse(clipDisplay(prompt, width-1)))
	buf := []byte{0}
	_, _ = os.Stdin.Read(buf)
	return buf[0] == 'y' || buf[0] == 'Y'
}

func commandRun(rest []string) error {
	accounts, err := getAccounts(false, false)
	if err != nil {
		return err
	}
	auth, _ := loadMap(authFile)
	var acc *account
	args := []string{}
	if len(rest) == 0 {
		acc, err = resolveSelector(nil, accounts, auth, false)
	} else if rest[0] == "--" {
		acc, err = resolveSelector(nil, accounts, auth, false)
		args = rest[1:]
	} else if strings.HasPrefix(rest[0], "-") {
		acc, err = resolveSelector(nil, accounts, auth, false)
		args = rest
	} else {
		acc, err = resolveSelector([]string{rest[0]}, accounts, auth, true)
		args = rest[1:]
	}
	if err != nil {
		return err
	}
	if acc == nil {
		return fmt.Errorf("no Claude account configured")
	}
	return execClaude(*acc, args)
}

func execClaude(acc account, args []string) error {
	cmd := exec.Command(claudeBin, args...)
	cmd.Env = accountEnv(acc)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runChild(cmd)
}

func runClaudeSubcommand(acc account, subcommand []string) error {
	cmd := exec.Command(claudeBin, subcommand...)
	cmd.Env = accountEnv(acc)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runChild(cmd)
}

func runChild(cmd *exec.Cmd) error {
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ExitCodeError(exitErr.ExitCode())
	}
	return err
}

type ExitCodeError int

func (e ExitCodeError) Error() string {
	return fmt.Sprintf("child exited with status %d", int(e))
}

func commandChange(rest []string) error {
	accounts, err := getAccounts(false, false)
	if err != nil {
		return err
	}
	auth, _ := loadMap(authFile)
	if len(accounts) == 0 {
		return fmt.Errorf("no Claude account configured")
	}
	var acc *account
	if len(rest) == 0 {
		if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
			action, err := picker(accounts, "change")
			if err != nil || action.Account == nil {
				return err
			}
			acc = action.Account
		} else {
			return printTable(accounts)
		}
	} else {
		acc, err = resolveSelector(rest, accounts, auth, true)
		if err != nil {
			return err
		}
	}
	if acc == nil {
		return nil
	}
	if err := setCurrentAccount(*acc); err != nil {
		return err
	}
	fmt.Printf("current Claude account: %s\n", displayAccountHint(*acc, auth))
	return nil
}

func commandDelete(rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("usage: agemux claude-accounts delete <selector>")
	}
	accounts, err := getAccounts(false, false)
	if err != nil {
		return err
	}
	auth, _ := loadMap(authFile)
	acc, err := resolveSelector(rest, accounts, auth, false)
	if err != nil {
		return err
	}
	if acc == nil {
		return fmt.Errorf("no Claude account configured")
	}
	label := displayAccountHint(*acc, auth)
	if err := removeAccount(*acc); err != nil {
		return err
	}
	remaining, err := getAccounts(false, false)
	if err != nil {
		return err
	}
	if err := persistResolvedCurrentAccount(remaining); err != nil {
		return err
	}
	fmt.Printf("deleted Claude account: %s\n", label)
	current := accountByID(currentAccountID(remaining), remaining)
	if current != nil {
		fmt.Printf("current Claude account: %s\n", displayAccountHint(*current, auth))
	} else {
		fmt.Println("no current Claude account")
	}
	return nil
}

func commandAdd(rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("usage: agemux claude-accounts add <config-dir>")
	}
	configDir := rest[len(rest)-1]
	acc := account{ID: accountIDForPath(configDir), ConfigDir: displayPath(configDir), Aliases: rest[:len(rest)-1]}
	if err := withLock(func() error {
		disk, _ := readAccountsUnlocked()
		for _, existing := range disk.Accounts {
			if resolvedPath(existing.ConfigDir) == resolvedPath(configDir) {
				return fmt.Errorf("account already exists: %s", displayPath(configDir))
			}
		}
		disk.Accounts = append(disk.Accounts, acc)
		disk.Accounts = dedupeAccounts(disk.Accounts)
		ignored := map[string]bool{}
		for _, path := range disk.IgnoredConfigDirs {
			ignored[resolvedPath(path)] = true
		}
		delete(ignored, resolvedPath(configDir))
		disk.IgnoredConfigDirs = sortedKeys(ignored)
		return saveJSON(accountsFile, disk, 0600)
	}); err != nil {
		return err
	}
	_ = installStatusline(acc, false)
	fmt.Printf("added Claude account: %s\n", displayPath(configDir))
	return nil
}

func commandLoginOrStatus(command string, rest []string) error {
	accounts, err := getAccounts(false, false)
	if err != nil {
		return err
	}
	auth, _ := loadMap(authFile)
	acc, err := resolveSelector(rest, accounts, auth, true)
	if err != nil {
		return err
	}
	if acc == nil {
		return fmt.Errorf("no Claude account configured")
	}
	return runClaudeSubcommand(*acc, []string{"auth", command})
}

func printCurrent() error {
	accounts, err := getAccounts(true, false)
	if err != nil {
		return err
	}
	current := currentAccountID(accounts)
	acc := accountByID(current, accounts)
	if acc == nil {
		fmt.Println("no current Claude account")
		return nil
	}
	auth, _ := loadMap(authFile)
	fmt.Printf("current Claude account: %s\n", displayAccountHint(*acc, auth))
	return nil
}

func commandEnv(rest []string) error {
	accounts, err := getAccounts(true, false)
	if err != nil {
		return err
	}
	current := currentAccountID(accounts)
	acc := accountByID(current, accounts)
	if acc == nil {
		return fmt.Errorf("no current Claude account")
	}
	if contains(rest, "--config-dir") {
		fmt.Println(resolvedPath(acc.ConfigDir))
		return nil
	}
	fmt.Printf("CLAUDE_CONFIG_DIR=%s\n", shellQuote(resolvedPath(acc.ConfigDir)))
	return nil
}

func normalizeSearch(text string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
		} else if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func accountSearchText(acc account, auth map[string]any, index int) string {
	parts := []string{strconv.Itoa(index + 1), acc.ID, acc.ConfigDir}
	parts = append(parts, acc.Aliases...)
	row := lookupRow(auth, acc)
	for _, key := range []string{"email", "orgName", "subscriptionType"} {
		if value, _ := row[key].(string); value != "" {
			parts = append(parts, value)
		}
	}
	return normalizeSearch(strings.Join(parts, " "))
}

func selectorIndex(selector []string) (int, bool) {
	if len(selector) != 1 {
		return 0, false
	}
	text := strings.ToLower(selector[0])
	if n, err := strconv.Atoi(text); err == nil {
		return n - 1, true
	}
	if n, ok := numberWords[text]; ok {
		return n - 1, true
	}
	return 0, false
}

func fuzzyMatches(accounts []account, auth map[string]any, selector []string) []int {
	query := normalizeSearch(strings.Join(selector, " "))
	if query == "" {
		return nil
	}
	if idx, ok := selectorIndex(selector); ok && idx >= 0 && idx < len(accounts) {
		return []int{idx}
	}
	type scored struct {
		idx   int
		score int
	}
	var scores []scored
	for i, acc := range accounts {
		text := accountSearchText(acc, auth, i)
		score := 0
		switch {
		case text == query:
			score = 1000
		case strings.HasPrefix(text, query):
			score = 900
		case strings.Contains(text, query):
			score = 700
		default:
			score = max(0, 500-levenshtein(query, text[:min(len(text), max(len(query)*2, 1))]))
		}
		if score > 0 {
			scores = append(scores, scored{i, score})
		}
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].score == scores[j].score {
			return scores[i].idx < scores[j].idx
		}
		return scores[i].score > scores[j].score
	})
	if len(scores) == 0 {
		return nil
	}
	best := scores[0].score
	var result []int
	for _, item := range scores {
		if item.score == best {
			result = append(result, item.idx)
		}
	}
	return result
}

func resolveSelector(selector []string, accounts []account, auth map[string]any, allowPicker bool) (*account, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	if len(selector) == 0 {
		current := currentAccountID(accounts)
		return accountByID(current, accounts), nil
	}
	matches := fuzzyMatches(accounts, auth, selector)
	if len(matches) == 1 {
		return &accounts[matches[0]], nil
	}
	if len(matches) > 1 && allowPicker && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		candidates := make([]account, 0, len(matches))
		for _, idx := range matches {
			candidates = append(candidates, accounts[idx])
		}
		action, err := picker(candidates, "resolve")
		if err != nil || action.Account == nil {
			return nil, err
		}
		return action.Account, nil
	}
	if len(matches) > 1 {
		fmt.Println("ambiguous account selector:")
		for _, idx := range matches {
			fmt.Printf("  %d  %s\n", idx+1, displayAccountHint(accounts[idx], auth))
		}
		return nil, fmt.Errorf("selector matched multiple accounts")
	}
	return nil, fmt.Errorf("no account matched: %s", strings.Join(selector, " "))
}

func installStatusline(acc account, quiet bool) error {
	accountDir := resolvedPath(acc.ConfigDir)
	if err := os.MkdirAll(accountDir, 0700); err != nil {
		return err
	}
	settingsPath := filepath.Join(accountDir, "settings.json")
	data := map[string]any{}
	_ = loadJSON(settingsPath, &data)
	command := statuslineCommand(acc)
	data["statusLine"] = map[string]any{"type": "command", "command": command, "padding": 0}
	mode := os.FileMode(0600)
	if st, err := os.Stat(settingsPath); err == nil {
		mode = st.Mode() & 0777
	}
	if err := saveJSON(settingsPath, data, mode); err != nil {
		return err
	}
	if !quiet {
		fmt.Printf("installed statusline for %s\n", displayAccountHint(acc, nil))
	}
	return nil
}

func resetAccountSettings(acc account) error {
	accountDir := resolvedPath(acc.ConfigDir)
	if err := os.MkdirAll(accountDir, 0700); err != nil {
		return err
	}
	command := statuslineCommand(acc)
	return saveJSON(filepath.Join(accountDir, "settings.json"), map[string]any{
		"statusLine": map[string]any{"type": "command", "command": command, "padding": 0},
	}, 0600)
}

func accountFromStatuslineArg(accountID string) account {
	accounts, _ := getAccounts(true, false)
	if accountID != "" {
		if acc := accountByID(accountID, accounts); acc != nil {
			return *acc
		}
		for _, acc := range accounts {
			for _, alias := range acc.Aliases {
				if alias == accountID {
					return acc
				}
			}
		}
	}
	if configDir := os.Getenv("CLAUDE_CONFIG_DIR"); configDir != "" {
		key := resolvedPath(configDir)
		for _, acc := range accounts {
			if resolvedPath(acc.ConfigDir) == key {
				return acc
			}
		}
		return account{ID: accountIDForPath(configDir), ConfigDir: displayPath(configDir), Aliases: []string{}}
	}
	return account{ID: "unknown", ConfigDir: "~/.claude", Aliases: []string{}}
}

func handleStatusline(argv []string) error {
	accountID := ""
	for i := 1; i < len(argv); i++ {
		if (argv[i] == "--account-id" || argv[i] == "--account") && i+1 < len(argv) {
			accountID = argv[i+1]
			i++
		}
	}
	acc := accountFromStatuslineArg(accountID)
	raw, _ := io.ReadAll(os.Stdin)
	data := parseJSONOutput(string(raw))
	if data == nil {
		fmt.Println(displayAccountHint(acc, nil))
		return nil
	}
	rateLimits, _ := data["rate_limits"].(map[string]any)
	if rateLimits == nil {
		rateLimits = map[string]any{}
	}
	model, _ := data["model"].(map[string]any)
	contextWindow, _ := data["context_window"].(map[string]any)
	_ = updateQuota(acc, map[string]any{
		"rate_limits":    rateLimits,
		"model":          model,
		"context_window": contextWindow,
		"session_id":     data["session_id"],
		"session_name":   data["session_name"],
		"version":        data["version"],
	})
	five := quotaWindow(map[string]any{"rate_limits": rateLimits}, "five_hour")
	seven := quotaWindow(map[string]any{"rate_limits": rateLimits}, "seven_day")
	modelName := "Claude"
	if display, _ := model["display_name"].(string); display != "" {
		modelName = display
	} else if id, _ := model["id"].(string); id != "" {
		modelName = id
	}
	text := strings.Join([]string{
		displayAccountHint(acc, nil),
		modelName,
		"sess " + fmtLeft(five["used_percentage"], true),
		"week " + fmtLeft(seven["used_percentage"], true),
	}, " | ")
	if contextWindow != nil {
		if _, ok := pctValue(contextWindow["used_percentage"]); ok {
			text += " | ctx " + fmtPct(contextWindow["used_percentage"], true)
		}
	}
	width := 80
	if rawCols := os.Getenv("COLUMNS"); rawCols != "" {
		if parsed, err := strconv.Atoi(rawCols); err == nil {
			width = parsed
		}
	}
	fmt.Println(clipDisplay(text, max(8, width-1)))
	return nil
}

func invokedScriptPath() string {
	if override := envDefaultAny([]string{"AGEMUX_BIN", "CLSW_BIN"}, ""); override != "" {
		return override
	}
	if filepath.IsAbs(os.Args[0]) {
		if abs, err := filepath.Abs(os.Args[0]); err == nil {
			return abs
		}
		return os.Args[0]
	}
	if strings.ContainsRune(os.Args[0], filepath.Separator) {
		if abs, err := filepath.Abs(os.Args[0]); err == nil {
			return abs
		}
	}
	if found, err := exec.LookPath(filepath.Base(os.Args[0])); err == nil {
		return found
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return os.Args[0]
}

func invokedBinDir() string {
	return filepath.Dir(invokedScriptPath())
}

func isManagedClaudeShim(path string) bool {
	return managedClaudeShimKind(path) != ""
}

func managedClaudeShimKind(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	switch {
	case bytes.Contains(content, []byte("agemux managed Claude wrapper")):
		return "agemux"
	case bytes.Contains(content, []byte("clsw managed Claude wrapper")):
		return "clsw"
	default:
		return ""
	}
}

func claudeRealNames() (string, string) {
	if runtime.GOOS == "windows" {
		return "claude.agemux-real.cmd", "claude.clsw-real.cmd"
	}
	return "claude.agemux-real", "claude.clsw-real"
}

func managedClaudeRealForShim(shimPath string) string {
	if !isManagedClaudeShim(shimPath) {
		return ""
	}
	binDir := filepath.Dir(shimPath)
	agemuxReal, legacyReal := claudeRealNames()
	for _, name := range []string{agemuxReal, legacyReal} {
		path := filepath.Join(binDir, name)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	return ""
}

func migrateLegacyClaudeReal(binDir string) (string, error) {
	agemuxReal, legacyReal := claudeRealNames()
	realPath := filepath.Join(binDir, agemuxReal)
	legacyPath := filepath.Join(binDir, legacyReal)
	if _, err := os.Stat(realPath); err == nil {
		return realPath, nil
	}
	if _, err := os.Stat(legacyPath); err == nil {
		if err := os.Rename(legacyPath, realPath); err != nil {
			return "", err
		}
		return realPath, nil
	}
	return realPath, nil
}

func preserveRealClaude(source, realPath string, move bool) error {
	if _, err := os.Stat(realPath); err == nil {
		return nil
	}
	if move {
		return os.Rename(source, realPath)
	}
	if runtime.GOOS == "windows" {
		content := fmt.Sprintf("@echo off\r\nrem agemux preserved Claude launcher\r\ncall %s %%*\r\n", quoteCmd(source))
		return atomicWrite(realPath, []byte(content), 0755)
	}
	target, err := filepath.Abs(source)
	if err != nil {
		target = source
	}
	return os.Symlink(target, realPath)
}

func commandInstallShim(rest []string) error {
	force := contains(rest, "--force")
	binDir := invokedBinDir()
	if idx := indexOf(rest, "--bin-dir"); idx >= 0 {
		if idx+1 >= len(rest) {
			return fmt.Errorf("--bin-dir requires a value")
		}
		binDir = expandPath(rest[idx+1])
	}
	shimName := "claude"
	realName := "claude.agemux-real"
	if runtime.GOOS == "windows" {
		shimName = "claude.cmd"
		realName = "claude.agemux-real.cmd"
	}
	shimPath := filepath.Join(binDir, shimName)
	realPath := filepath.Join(binDir, realName)
	shimKind := ""
	shimExists := false
	if _, err := os.Stat(shimPath); err == nil {
		shimExists = true
		shimKind = managedClaudeShimKind(shimPath)
	}
	if shimExists && shimKind == "agemux" && !force {
		fmt.Printf("Claude shim already installed: %s\n", shimPath)
		return nil
	}
	if shimExists && shimKind == "" {
		if !force {
			return fmt.Errorf("refusing to replace existing %s; rerun install-shim --force", shimPath)
		}
		if _, err := os.Stat(realPath); err == nil {
			backup := fmt.Sprintf("%s.bak-%s", realPath, time.Now().Format("20060102-150405"))
			if err := os.Rename(realPath, backup); err != nil {
				return err
			}
			fmt.Printf("backed up existing real Claude path: %s\n", backup)
		}
		if err := preserveRealClaude(shimPath, realPath, true); err != nil {
			return err
		}
	}
	if !shimExists || shimKind != "" {
		migratedRealPath, err := migrateLegacyClaudeReal(binDir)
		if err != nil {
			return err
		}
		realPath = migratedRealPath
	}
	if _, err := os.Stat(realPath); errors.Is(err, os.ErrNotExist) {
		target, err := exec.LookPath("claude")
		if err != nil || resolvedPath(target) == resolvedPath(shimPath) {
			return fmt.Errorf("could not find an existing Claude CLI on PATH")
		}
		if err := preserveRealClaude(target, realPath, false); err != nil {
			return err
		}
	}
	agemuxPath := invokedScriptPath()
	if runtime.GOOS == "windows" {
		content := fmt.Sprintf(`@echo off
rem agemux managed Claude wrapper
setlocal
set "REAL_CLAUDE=%s"
if "%%CLAUDE_CONFIG_DIR%%"=="" (
  for /f "usebackq delims=" %%%%i in (`+"`%s claude-accounts env --config-dir 2^>nul`"+`) do set "CLAUDE_CONFIG_DIR=%%%%i"
)
endlocal & set "CLAUDE_CONFIG_DIR=%%CLAUDE_CONFIG_DIR%%" & call "%%REAL_CLAUDE%%" %%*
`, realPath, quoteCmd(agemuxPath))
		return atomicWrite(shimPath, []byte(content), 0755)
	}
	content := fmt.Sprintf(`#!/usr/bin/env bash
# agemux managed Claude wrapper
set -euo pipefail

AGEMUX_BIN=${AGEMUX_BIN:-%s}
REAL_CLAUDE=${AGEMUX_CLAUDE_REAL:-%s}

if [[ ! -x "$REAL_CLAUDE" ]]; then
  echo "claude wrapper: real Claude binary not found: $REAL_CLAUDE" >&2
  exit 127
fi

if [[ -z "${CLAUDE_CONFIG_DIR:-}" && -x "$AGEMUX_BIN" ]]; then
  config_dir="$("$AGEMUX_BIN" claude-accounts env --config-dir 2>/dev/null || true)"
  if [[ -n "$config_dir" ]]; then
    export CLAUDE_CONFIG_DIR="$config_dir"
  fi
fi

exec "$REAL_CLAUDE" "$@"
`, shellQuote(agemuxPath), shellQuote(realPath))
	if err := atomicWrite(shimPath, []byte(content), 0755); err != nil {
		return err
	}
	_ = os.Chmod(shimPath, 0755)
	fmt.Printf("installed Claude shim: %s\n", shimPath)
	fmt.Printf("real Claude CLI: %s\n", realPath)
	return nil
}

func commandUninstallShim(rest []string) error {
	binDir := invokedBinDir()
	if idx := indexOf(rest, "--bin-dir"); idx >= 0 {
		if idx+1 >= len(rest) {
			return fmt.Errorf("--bin-dir requires a value")
		}
		binDir = expandPath(rest[idx+1])
	}
	shimName := "claude"
	realName := "claude.agemux-real"
	if runtime.GOOS == "windows" {
		shimName = "claude.cmd"
		realName = "claude.agemux-real.cmd"
	}
	shimPath := filepath.Join(binDir, shimName)
	realPath := filepath.Join(binDir, realName)
	if !isManagedClaudeShim(shimPath) {
		return fmt.Errorf("no agemux-managed Claude shim found at %s", shimPath)
	}
	if _, err := os.Stat(realPath); err != nil {
		_, legacyReal := claudeRealNames()
		legacyPath := filepath.Join(binDir, legacyReal)
		if _, legacyErr := os.Stat(legacyPath); legacyErr != nil {
			return fmt.Errorf("real Claude path missing: %s", realPath)
		}
		realPath = legacyPath
	}
	if err := os.Remove(shimPath); err != nil {
		return err
	}
	if err := os.Rename(realPath, shimPath); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		fmt.Printf("restored Claude launcher: %s\n", shimPath)
	} else {
		fmt.Printf("restored Claude CLI: %s\n", shimPath)
	}
	return nil
}

func statuslineCommand(acc account) string {
	args := []string{invokedScriptPath(), "claude-accounts", "statusline", "--account-id", acc.ID}
	if runtime.GOOS == "windows" {
		quoted := make([]string, 0, len(args))
		for _, arg := range args {
			quoted = append(quoted, quoteCmd(arg))
		}
		return strings.Join(quoted, " ")
	}
	return shellJoin(args)
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return !(r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '+' || r == '=' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func quoteCmd(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func bold(s string) string    { return "\033[1m" + s + "\033[0m" }
func dim(s string) string     { return "\033[2m" + s + "\033[0m" }
func reverse(s string) string { return "\033[7m" + s + "\033[0m" }

func contains(items []string, target string) bool {
	return indexOf(items, target) >= 0
}

func indexOf(items []string, target string) int {
	for i, item := range items {
		if item == target {
			return i
		}
	}
	return -1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func levenshtein(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ca := range ar {
		cur := make([]int, len(br)+1)
		cur[0] = i + 1
		for j, cb := range br {
			cost := 0
			if ca != cb {
				cost = 1
			}
			cur[j+1] = min(min(cur[j]+1, prev[j+1]+1), prev[j]+cost)
		}
		prev = cur
	}
	return prev[len(br)]
}
