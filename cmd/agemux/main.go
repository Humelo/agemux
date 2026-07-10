//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	claudeaccounts "github.com/Humelo/agemux/internal/claudeaccounts"
	"github.com/creack/pty"
	"github.com/gofrs/flock"
	"golang.org/x/term"
)

const (
	codexKeyboardSetup       = "\033[?2004h\033[>4;0m\033[>7u\033[?1004h"
	codexKeyboardReset       = "\033[?1004l\033[?2004l\033[<u"
	defaultShpoolListTimeout = 5 * time.Second
)

var (
	prefix    = envDefaultAny([]string{"AGEMUX_PREFIX", "AGENTMUX_PREFIX"}, "agemux")
	dataDir   = expandPath(envDefault("AGEMUX_DATA_DIR", defaultDataDir()))
	metaFile  = filepath.Join(dataDir, "sessions.json")
	lockFile  = filepath.Join(dataDir, "sessions.lock")
	titleRE   = regexp.MustCompile(`\x1b\](?:0|2);([^\x07\x1b]*)(?:\x07|\x1b\\)`)
	nameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@+-]*$`)
	shpoolBin = resolveBinary("AGEMUX_SHPOOL_BIN", "/home/linuxbrew/.linuxbrew/bin/shpool", "shpool")
	codexBin  = resolveBinary("AGEMUX_CODEX_BIN", filepath.Join(homeDir(), ".local/bin/codex"), "codex")
)

type metadata map[string]map[string]any

type shpoolList struct {
	Sessions []map[string]any `json:"sessions"`
}

type menuItem struct {
	Type   string
	Name   string
	Label  string
	Detail string
}

type codexAccount struct {
	Name    string
	Path    string
	Email   string
	Current bool
	Updated string
	Usage   codexUsageSummary
}

type codexUsageSummary struct {
	Plan      string
	Primary   string
	Secondary string
	Credits   string
	Coupons   string
	Updated   string
	Error     string
}

type codexAccountAction struct {
	Action  string
	Account *codexAccount
}

func main() {
	if err := runMain(os.Args); err != nil {
		if code, ok := err.(claudeaccounts.ExitCodeError); ok {
			os.Exit(int(code))
		}
		if code, ok := err.(exitCodeError); ok {
			os.Exit(int(code))
		}
		fmt.Fprintln(os.Stderr, "agemux:", err)
		os.Exit(1)
	}
}

func runMain(argv []string) error {
	prog := filepath.Base(argv[0])
	if len(argv) == 1 {
		return interactive()
	}
	cmd := argv[1]
	switch {
	case cmd == "-h" || cmd == "--help" || cmd == "help":
		usage(prog)
	case cmd == "codex" || cmd == "new":
		return create("codex-resume")
	case cmd == "codex-new" || cmd == "fresh":
		return create("codex-fresh")
	case cmd == "claude":
		return create("claude-resume")
	case cmd == "claude-new":
		return create("claude-fresh")
	case cmd == "codex-accounts":
		return codexAccountsCommand(argv[2:])
	case cmd == "claude-accounts":
		return claudeaccounts.RunMain(append([]string{"agemux claude-accounts"}, argv[2:]...))
	case cmd == "list":
		return printList()
	case cmd == "attach" && len(argv) == 3:
		return execAttach(argv[2], "", false)
	case cmd == "attach" && len(argv) == 4 && (argv[2] == "--force" || argv[2] == "-f"):
		return execAttach(argv[3], "", true)
	case cmd == "kill" && len(argv) == 3:
		return killSession(argv[2])
	case cmd == "run" && len(argv) == 5:
		return runAgentSession(argv[2], argv[3], argv[4])
	default:
		usage(prog)
		return fmt.Errorf("unknown command")
	}
	return nil
}

func usage(prog string) {
	fmt.Printf(`Usage:
  %[1]s                  interactive session picker
  %[1]s codex            new shpool session running Codex resume picker
  %[1]s codex-new        new shpool session running fresh Codex
  %[1]s claude           new shpool session running Claude resume picker
  %[1]s claude-new       new shpool session running fresh Claude
  %[1]s codex-accounts   open the Codex account switcher
  %[1]s codex-accounts new [name]
  %[1]s codex-accounts change SELECTOR
  %[1]s codex-accounts delete SELECTOR
  %[1]s claude-accounts  open the Claude account switcher
  %[1]s list             list live agemux shpool sessions
  %[1]s attach NAME      attach to a live session
  %[1]s attach --force NAME
  %[1]s kill NAME        kill a session

Interactive keys: Arrows, Enter, c, C, l, L, k kill, q/Esc.
Close the VS Code terminal tab to detach without killing the agent.
Already-attached sessions are not force-detached by default; use attach --force intentionally.
Codex and Claude run with their dangerous permission bypass flags by default.
Set AGEMUX_CODEX_DANGEROUS=0 or AGEMUX_CLAUDE_DANGEROUS=0 to disable them.
Set AGEMUX_ALT_SCREEN=1 to use Codex's alternate screen mode.
Set AGEMUX_CODEX_BIN, AGEMUX_CLAUDE_BIN, or AGEMUX_SHPOOL_BIN to override binary paths.
`, prog)
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
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

func defaultDataDir() string {
	return filepath.Join(homeDir(), ".local/share/agemux")
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~") {
		return filepath.Join(homeDir(), strings.TrimPrefix(path, "~"))
	}
	return os.ExpandEnv(path)
}

func resolveBinary(envName, defaultPath, fallback string) string {
	if value := os.Getenv(envName); value != "" {
		return value
	}
	if st, err := os.Stat(defaultPath); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
		return defaultPath
	}
	if found, err := exec.LookPath(fallback); err == nil {
		return found
	}
	return fallback
}

func truthyEnv(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func defaultDangerousEnv(name string) bool {
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	if d, err := time.ParseDuration(value); err == nil && d > 0 {
		return d
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func executablePath() string {
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
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	if found, err := exec.LookPath(os.Args[0]); err == nil {
		if abs, err := filepath.Abs(found); err == nil {
			return abs
		}
		return found
	}
	return os.Args[0]
}

func nowName() string {
	random := make([]byte, 2)
	_, _ = rand.Read(random)
	return fmt.Sprintf("%s-%s-%d-%s", prefix, time.Now().Format("20060102-150405-000"), os.Getpid(), hex.EncodeToString(random))
}

func ensureName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("bad session name: %q", name)
	}
	return nil
}

func withMetaLock(fn func(metadata) error) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	_ = os.Chmod(dataDir, 0700)
	lock := flock.New(lockFile)
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	meta, err := loadMetaUnlocked()
	if err != nil {
		return err
	}
	return fn(meta)
}

func loadMetaUnlocked() (metadata, error) {
	content, err := os.ReadFile(metaFile)
	if errors.Is(err, os.ErrNotExist) {
		return loadLegacyMetaUnlocked()
	}
	if err != nil {
		return nil, err
	}
	var meta metadata
	if err := json.Unmarshal(content, &meta); err != nil {
		stamp := time.Now().Format("20060102-150405")
		_ = os.Rename(metaFile, filepath.Join(dataDir, fmt.Sprintf("sessions.json.corrupt-%s", stamp)))
		return metadata{}, nil
	}
	if meta == nil {
		meta = metadata{}
	}
	return meta, nil
}

func loadLegacyMetaUnlocked() (metadata, error) {
	legacy := filepath.Join(homeDir(), ".local/share/agentmux/sessions.json")
	if filepath.Clean(legacy) == filepath.Clean(metaFile) {
		return metadata{}, nil
	}
	content, err := os.ReadFile(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return metadata{}, nil
	}
	if err != nil {
		return nil, err
	}
	var meta metadata
	if err := json.Unmarshal(content, &meta); err != nil {
		return metadata{}, nil
	}
	if meta == nil {
		meta = metadata{}
	}
	return meta, nil
}

func saveMetaUnlocked(meta metadata) error {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	tmp := filepath.Join(dataDir, fmt.Sprintf(".sessions.json.%d.%d.tmp", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(tmp, content, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, metaFile)
}

func updateMeta(name string, fields map[string]any) error {
	return withMetaLock(func(meta metadata) error {
		row := meta[name]
		if row == nil {
			row = map[string]any{}
		}
		for key, value := range fields {
			row[key] = value
		}
		meta[name] = row
		return saveMetaUnlocked(meta)
	})
}

func shpoolSessions() ([]map[string]any, error) {
	timeout := durationEnv("AGEMUX_SHPOOL_LIST_TIMEOUT", defaultShpoolListTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shpoolBin, "list", "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s list --json timed out after %s; shpool may be wedged by a stale attached client", shpoolBin, timeout)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("missing shpool. Install shpool to use agemux persistent sessions")
		}
		return nil, fmt.Errorf("%s list --json failed: %s", shpoolBin, strings.TrimSpace(string(out)))
	}
	var data shpoolList
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("%s list --json returned invalid JSON: %w", shpoolBin, err)
	}
	return data.Sessions, nil
}

func agemuxSessions() ([]map[string]any, error) {
	sessions, err := shpoolSessions()
	if err != nil {
		return nil, err
	}
	liveNames := map[string]bool{}
	var live []map[string]any
	err = withMetaLock(func(meta metadata) error {
		for _, sess := range sessions {
			name, _ := sess["name"].(string)
			if !isAgemuxSessionName(name) {
				continue
			}
			liveNames[name] = true
			row := map[string]any{}
			for key, value := range sess {
				row[key] = value
			}
			row["meta"] = meta[name]
			live = append(live, row)
		}
		changed := false
		for name := range meta {
			if isAgemuxSessionName(name) && !liveNames[name] {
				delete(meta, name)
				changed = true
			}
		}
		if changed {
			return saveMetaUnlocked(meta)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(live, func(i, j int) bool {
		return int64Value(live[i]["started_at_unix_ms"]) > int64Value(live[j]["started_at_unix_ms"])
	})
	return live, nil
}

func isAgemuxSessionName(name string) bool {
	if strings.HasPrefix(name, prefix+"-") {
		return true
	}
	return prefix != "agentmux" && strings.HasPrefix(name, "agentmux-")
}

func liveSessionNames() (map[string]bool, error) {
	states, err := liveSessionStates()
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for name := range states {
		names[name] = true
	}
	return names, nil
}

func liveSessionStates() (map[string]string, error) {
	sessions, err := shpoolSessions()
	if err != nil {
		return nil, err
	}
	states := map[string]string{}
	for _, sess := range sessions {
		if name, _ := sess["name"].(string); name != "" {
			states[name] = strings.ToLower(stringValue(sess["status"]))
		}
	}
	return states, nil
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func rootDir() string {
	if root := os.Getenv("AGEMUX_ROOT"); root != "" {
		if abs, err := filepath.Abs(expandPath(root)); err == nil {
			return abs
		}
		return expandPath(root)
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func splitKind(kind string) (string, string) {
	provider, mode, ok := strings.Cut(kind, "-")
	if !ok {
		return "codex", kind
	}
	return provider, mode
}

func agentArgs(kind, root string) ([]string, error) {
	provider, mode := splitKind(kind)
	switch {
	case provider == "codex" && (mode == "resume" || mode == "fresh"):
		args := []string{codexBin}
		if defaultDangerousEnv("AGEMUX_CODEX_DANGEROUS") {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		if !truthyEnv("AGEMUX_ALT_SCREEN") {
			args = append(args, "--no-alt-screen")
		}
		args = append(args, "-C", root)
		if mode == "resume" {
			args = append(args, "resume")
		}
		return args, nil
	case provider == "claude" && (mode == "resume" || mode == "fresh"):
		args := []string{executablePath(), "claude-accounts", "run", "--"}
		if defaultDangerousEnv("AGEMUX_CLAUDE_DANGEROUS") {
			args = append(args, "--dangerously-skip-permissions")
		}
		if mode == "resume" {
			args = append(args, "--resume")
		}
		return args, nil
	default:
		return nil, fmt.Errorf("bad run kind: %q", kind)
	}
}

func agentLabel(kind string) string {
	switch kind {
	case "codex-resume":
		return "Codex"
	case "codex-fresh":
		return "Codex new"
	case "claude-resume":
		return "Claude"
	case "claude-fresh":
		return "Claude new"
	default:
		return kind
	}
}

func agentCommand(kind, root string) string {
	args, _ := agentArgs(kind, root)
	return shellJoin(args)
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			quoted = append(quoted, "''")
			continue
		}
		if strings.IndexFunc(arg, func(r rune) bool {
			return !(r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '+' || r == '=' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
		}) == -1 {
			quoted = append(quoted, arg)
		} else {
			quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'"'"'`)+"'")
		}
	}
	return strings.Join(quoted, " ")
}

func terminalTitle(title string) {
	fmt.Printf("\033]0;%s\a", title)
}

func cleanTitle(title string) string {
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(title, "codex ")
	title = strings.TrimPrefix(title, "claude ")
	if title == "" {
		return ""
	}
	if len([]rune(title)) > 120 {
		runes := []rune(title)
		title = string(runes[:120])
	}
	return title
}

func registerSession(name, kind, root string) error {
	provider, _ := splitKind(kind)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return updateMeta(name, map[string]any{
		"provider":   provider,
		"kind":       kind,
		"root":       root,
		"title":      agentLabel(kind),
		"created_at": now,
	})
}

func sessionMeta(name string) map[string]any {
	meta, err := loadMetaUnlocked()
	if err != nil {
		return map[string]any{}
	}
	if row := meta[name]; row != nil {
		return row
	}
	return map[string]any{}
}

func sessionTitle(name string) string {
	row := sessionMeta(name)
	if title, _ := row["title"].(string); title != "" {
		return title
	}
	if kind, _ := row["kind"].(string); kind != "" {
		return agentLabel(kind)
	}
	return name
}

func sessionKind(name string) string {
	row := sessionMeta(name)
	if kind, _ := row["kind"].(string); kind != "" {
		return kind
	}
	return "codex-resume"
}

func emitSessionTitle(name string) {
	terminalTitle(sessionTitle(name))
}

func emitCodexKeyboardSetup() {
	fmt.Print(codexKeyboardSetup)
}

func emitCodexKeyboardReset() {
	fmt.Print(codexKeyboardReset)
}

func runCommand(name, kind, root string) string {
	envKeys := []string{
		"AGEMUX_ALT_SCREEN",
		"AGEMUX_CLAUDE_ACCOUNTS_DATA_DIR",
		"AGEMUX_CLAUDE_BIN",
		"AGEMUX_CLAUDE_DANGEROUS",
		"AGEMUX_CODEX_BIN",
		"AGEMUX_CODEX_DANGEROUS",
		"AGEMUX_DATA_DIR",
		"AGEMUX_PREFIX",
		"AGEMUX_SHPOOL_BIN",
		"CLSW_DATA_DIR",
		"CODEX_HOME",
	}
	var args []string
	hasEnv := false
	for _, key := range envKeys {
		if value, ok := os.LookupEnv(key); ok {
			args = append(args, key+"="+value)
			hasEnv = true
		}
	}
	args = append(args, executablePath(), "run", name, kind, root)
	if !hasEnv {
		return shellJoin(args)
	}
	return shellJoin(append([]string{"/usr/bin/env"}, args...))
}

func execAttach(name, createKind string, force bool) error {
	if err := ensureName(name); err != nil {
		return err
	}
	args := []string{shpoolBin, "attach"}
	if force {
		args = append(args, "-f")
	}
	kind := createKind
	if createKind != "" {
		root := rootDir()
		if err := registerSession(name, createKind, root); err != nil {
			return err
		}
		args = append(args, "--dir", root, "--cmd", runCommand(name, createKind, root))
	} else {
		states, err := liveSessionStates()
		if err != nil {
			return err
		}
		status, ok := states[name]
		if !ok {
			return fmt.Errorf("no live agemux session named %q", name)
		}
		if status == "attached" && !force {
			return fmt.Errorf("session %q is already attached; refusing implicit force-detach because stale VS Code/SSH clients can wedge shpool. Close the old terminal or run `agemux attach --force %s` intentionally", name, name)
		}
		emitSessionTitle(name)
		kind = sessionKind(name)
	}
	args = append(args, "--", name)
	provider, _ := splitKind(kind)
	if provider == "codex" {
		emitCodexKeyboardSetup()
		defer emitCodexKeyboardReset()
	}
	err := runForeground(args)
	if err == nil {
		return nil
	}
	return diagnoseAttachFailure(name, err)
}

func diagnoseAttachFailure(name string, attachErr error) error {
	var code exitCodeError
	if !errors.As(attachErr, &code) || int(code) != 1 {
		return attachErr
	}
	states, err := liveSessionStates()
	if err != nil {
		return attachErr
	}
	status, live := states[name]
	if !live {
		return attachErr
	}
	return fmt.Errorf(
		"session %q is still live (%s), but shpool attach exited with status 1; terminal transport was interrupted or wedged while the agent may still be running. Reopen the session; if it exits immediately again, inspect the active work before killing it",
		name,
		status,
	)
}

func runForeground(args []string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runForegroundCommand(cmd)
}

func runForegroundCommand(cmd *exec.Cmd) error {
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitCodeError(exitErr.ExitCode())
	}
	return err
}

func create(kind string) error {
	names, err := liveSessionNames()
	if err != nil {
		return err
	}
	name := nowName()
	for names[name] {
		name = nowName()
	}
	return execAttach(name, kind, false)
}

func printList() error {
	sessions, err := agemuxSessions()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("No agemux sessions.")
		return nil
	}
	for _, sess := range sessions {
		meta, _ := sess["meta"].(map[string]any)
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n",
			stringValue(sess["name"]),
			stringValue(sess["status"]),
			stringValue(meta["kind"]),
			stringValue(meta["title"]),
			stringValue(meta["root"]),
		)
	}
	return nil
}

func codexAccountsCommand(args []string) error {
	if len(args) == 0 {
		return codexAccountsInteractive()
	}
	switch args[0] {
	case "current":
		accounts, err := listCodexAccounts(false)
		if err != nil {
			return err
		}
		acc := currentCodexAccount(accounts)
		if acc == nil {
			fmt.Println("no current Codex account")
			return nil
		}
		fmt.Printf("current Codex account: %s\n", codexAccountLabel(*acc))
		return nil
	case "new":
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		return commandNewCodexAccount(name)
	case "change", "select", "use":
		accounts, err := listCodexAccounts(false)
		if err != nil {
			return err
		}
		acc, err := resolveCodexAccountSelector(accounts, args[1:])
		if err != nil {
			return err
		}
		if acc == nil {
			return fmt.Errorf("no Codex account configured")
		}
		if err := switchCodexAccount(*acc); err != nil {
			return err
		}
		fmt.Printf("current Codex account: %s\n", codexAccountLabel(*acc))
		return nil
	case "delete", "remove", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: agemux codex-accounts delete SELECTOR")
		}
		accounts, err := listCodexAccounts(false)
		if err != nil {
			return err
		}
		acc, err := resolveCodexAccountSelector(accounts, args[1:])
		if err != nil {
			return err
		}
		if acc == nil {
			return fmt.Errorf("no Codex account configured")
		}
		return deleteCodexAccountWithMessage(*acc)
	case "login":
		accounts, err := listCodexAccounts(false)
		if err != nil {
			return err
		}
		acc, err := resolveCodexAccountSelector(accounts, args[1:])
		if err != nil {
			return err
		}
		if acc == nil {
			return commandNewCodexAccount("")
		}
		return reloginCodexAccount(*acc)
	case "status":
		accounts, err := listCodexAccounts(false)
		if err != nil {
			return err
		}
		acc, err := resolveCodexAccountSelector(accounts, args[1:])
		if err != nil {
			return err
		}
		if acc == nil {
			return fmt.Errorf("no Codex account configured")
		}
		return runCodexSubcommandWithAccount(*acc, []string{"login", "status"})
	case "refresh":
		accounts, err := listCodexAccounts(false)
		if err != nil {
			return err
		}
		enrichCodexAccountUsage(accounts)
		return printCodexAccounts(accounts)
	case "list":
		accounts, err := listCodexAccounts(false)
		if err != nil {
			return err
		}
		enrichCodexAccountUsage(accounts)
		return printCodexAccounts(accounts)
	default:
		return fmt.Errorf("usage: agemux codex-accounts [list|refresh|current|change SELECTOR|new [name]|login [selector]|status [selector]|delete SELECTOR]")
	}
}

func codexAccountsInteractive() error {
	accounts, err := listCodexAccounts(false)
	if err != nil {
		return err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) || os.Getenv("TERM") == "dumb" {
		enrichCodexAccountUsage(accounts)
		if len(accounts) == 0 {
			fmt.Printf("No Codex account files found in %s\n", codexHomeDir())
			fmt.Println("Run `agemux codex-accounts new` to add one.")
			return nil
		}
		return printCodexAccounts(accounts)
	}
	if len(accounts) > 0 {
		runCodexLoadingRefresh(accounts)
	}
	action, err := codexAccountPicker(accounts)
	if err != nil || action.Action == "" {
		return err
	}
	if action.Action == "new" {
		return commandNewCodexAccount("")
	}
	if action.Account == nil {
		return nil
	}
	switch action.Action {
	case "change":
		if err := switchCodexAccount(*action.Account); err != nil {
			return err
		}
		fmt.Printf("current Codex account: %s\n", codexAccountLabel(*action.Account))
	case "login":
		return reloginCodexAccount(*action.Account)
	case "status":
		return runCodexSubcommandWithAccount(*action.Account, []string{"login", "status"})
	}
	return nil
}

func codexAccountLabel(acc codexAccount) string {
	label := acc.Name
	if acc.Email != "" {
		label += " <" + acc.Email + ">"
	}
	return label
}

func currentCodexAccount(accounts []codexAccount) *codexAccount {
	for i := range accounts {
		if accounts[i].Current {
			return &accounts[i]
		}
	}
	return nil
}

func resolveCodexAccountSelector(accounts []codexAccount, selector []string) (*codexAccount, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	query := normalizeSearch(strings.Join(selector, " "))
	if query == "" {
		return currentCodexAccount(accounts), nil
	}
	if len(selector) == 1 {
		if idx, ok := selectorIndex(selector[0]); ok {
			if idx < 0 || idx >= len(accounts) {
				return nil, fmt.Errorf("Codex account index out of range: %s", selector[0])
			}
			return &accounts[idx], nil
		}
	}
	matches := matchCodexAccounts(accounts, query)
	if len(matches) == 1 {
		return &accounts[matches[0]], nil
	}
	if len(matches) > 1 {
		var lines []string
		for _, idx := range matches {
			lines = append(lines, fmt.Sprintf("  %d  %s", idx+1, codexAccountLabel(accounts[idx])))
		}
		return nil, fmt.Errorf("ambiguous Codex account selector:\n%s", strings.Join(lines, "\n"))
	}
	return nil, fmt.Errorf("no Codex account matches %q", strings.Join(selector, " "))
}

func selectorIndex(text string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, false
	}
	return n - 1, true
}

func matchCodexAccounts(accounts []codexAccount, query string) []int {
	type scored struct {
		idx   int
		score int
	}
	var scores []scored
	for i, acc := range accounts {
		text := normalizeSearch(strings.Join([]string{
			strconv.Itoa(i + 1),
			acc.Name,
			acc.Email,
			filepath.Base(acc.Path),
		}, " "))
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
	var matches []int
	for _, score := range scores {
		if score.score == best {
			matches = append(matches, score.idx)
		}
	}
	return matches
}

func normalizeSearch(text string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(text) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastSpace = false
		} else if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
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
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 0
			if ar[i-1] != br[j-1] {
				cost = 1
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func reloginCodexAccount(acc codexAccount) error {
	tempDir, err := os.MkdirTemp("", "agemux-codex-login-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	content, err := os.ReadFile(acc.Path)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(tempDir, "auth.json"), content, 0600); err != nil {
		return err
	}
	fmt.Printf("starting Codex login for %s...\n", codexAccountLabel(acc))
	cmd := exec.Command(codexBin, "login")
	cmd.Env = upsertEnv(os.Environ(), "CODEX_HOME", tempDir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := runForegroundCommand(cmd); err != nil {
		return err
	}
	updated, err := os.ReadFile(filepath.Join(tempDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("Codex login did not create auth.json: %w", err)
	}
	if len(bytes.TrimSpace(updated)) == 0 {
		return fmt.Errorf("Codex login created an empty auth.json")
	}
	if err := writeFileAtomic(acc.Path, updated, 0600); err != nil {
		return err
	}
	if acc.Current {
		if err := writeActiveCodexAuth(updated); err != nil {
			return err
		}
	}
	acc.Email = codexAuthEmail(updated)
	fmt.Printf("updated Codex account: %s\n", codexAccountLabel(acc))
	return nil
}

func runCodexSubcommandWithAccount(acc codexAccount, args []string) error {
	tempDir, err := os.MkdirTemp("", "agemux-codex-account-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	content, err := os.ReadFile(acc.Path)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(tempDir, "auth.json"), content, 0600); err != nil {
		return err
	}
	cmd := exec.Command(codexBin, args...)
	cmd.Env = upsertEnv(os.Environ(), "CODEX_HOME", tempDir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runForegroundCommand(cmd)
}

func deleteCodexAccountWithMessage(acc codexAccount) error {
	next, err := deleteCodexAccount(acc)
	if err != nil {
		return err
	}
	fmt.Printf("deleted Codex account: %s\n", codexAccountLabel(acc))
	if next != nil {
		fmt.Printf("current Codex account: %s\n", codexAccountLabel(*next))
	} else {
		fmt.Println("no current Codex account")
	}
	return nil
}

func deleteCodexAccount(acc codexAccount) (*codexAccount, error) {
	content, err := os.ReadFile(acc.Path)
	if err != nil {
		return nil, err
	}
	wasCurrent := isActiveCodexAuth(content)
	if err := os.Remove(acc.Path); err != nil {
		return nil, err
	}
	if !wasCurrent {
		return currentCodexAccountAfterReload()
	}
	remaining, err := listCodexAccounts(false)
	if err != nil {
		return nil, err
	}
	if len(remaining) > 0 {
		next := remaining[0]
		nextContent, err := os.ReadFile(next.Path)
		if err != nil {
			return nil, err
		}
		if err := writeActiveCodexAuth(nextContent); err != nil {
			return nil, err
		}
		next.Current = true
		return &next, nil
	}
	activePath := filepath.Join(codexHomeDir(), "auth.json")
	if isActiveCodexAuth(content) {
		if err := os.Remove(activePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, nil
}

func currentCodexAccountAfterReload() (*codexAccount, error) {
	accounts, err := listCodexAccounts(false)
	if err != nil {
		return nil, err
	}
	return currentCodexAccount(accounts), nil
}

func isActiveCodexAuth(content []byte) bool {
	current, err := os.ReadFile(filepath.Join(codexHomeDir(), "auth.json"))
	return err == nil && bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(content))
}

func codexHomeDir() string {
	return expandPath(envDefault("CODEX_HOME", filepath.Join(homeDir(), ".codex")))
}

func commandNewCodexAccount(name string) error {
	name = strings.TrimSpace(name)
	if name != "" {
		if err := validateCodexAccountName(name); err != nil {
			return err
		}
		if _, err := os.Stat(codexAccountPath(name)); err == nil {
			return fmt.Errorf("Codex account %q already exists", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	tempDir, err := os.MkdirTemp("", "agemux-codex-login-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	fmt.Println("starting Codex login for the new account...")
	cmd := exec.Command(codexBin, "login")
	cmd.Env = upsertEnv(os.Environ(), "CODEX_HOME", tempDir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := runForegroundCommand(cmd); err != nil {
		return err
	}

	content, err := os.ReadFile(filepath.Join(tempDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("Codex login did not create auth.json: %w", err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return fmt.Errorf("Codex login created an empty auth.json")
	}
	if name == "" {
		defaultName := uniqueCodexAccountName(sanitizeCodexAccountName(codexAuthEmail(content)))
		if defaultName == "" {
			defaultName = uniqueCodexAccountName("account-" + time.Now().Format("20060102-150405"))
		}
		name, err = promptCodexAccountName(defaultName)
		if err != nil {
			return err
		}
	}
	acc, err := saveCodexAccount(name, content)
	if err != nil {
		return err
	}
	if err := switchCodexAccount(acc); err != nil {
		return err
	}
	fmt.Printf("current Codex account: %s\n", codexAccountLabel(acc))
	return nil
}

func promptCodexAccountName(defaultName string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return defaultName, nil
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("Codex account name [%s]: ", defaultName)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		name := strings.TrimSpace(line)
		if name == "" {
			name = defaultName
		}
		if err := validateCodexAccountName(name); err != nil {
			fmt.Println(err)
			continue
		}
		if _, err := os.Stat(codexAccountPath(name)); err == nil {
			fmt.Printf("Codex account %q already exists.\n", name)
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return name, nil
	}
}

func saveCodexAccount(name string, content []byte) (codexAccount, error) {
	if err := validateCodexAccountName(name); err != nil {
		return codexAccount{}, err
	}
	dir := codexHomeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return codexAccount{}, err
	}
	target := codexAccountPath(name)
	if _, err := os.Stat(target); err == nil {
		return codexAccount{}, fmt.Errorf("Codex account %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return codexAccount{}, err
	}
	if err := writeFileAtomic(target, content, 0600); err != nil {
		return codexAccount{}, err
	}
	updated := "-"
	if st, err := os.Stat(target); err == nil {
		updated = st.ModTime().Format("01-02 15:04")
	}
	return codexAccount{
		Name:    name,
		Path:    target,
		Email:   codexAuthEmail(content),
		Updated: updated,
	}, nil
}

func codexAccountPath(name string) string {
	return filepath.Join(codexHomeDir(), "auth."+name+".json")
}

func validateCodexAccountName(name string) error {
	if name == "" {
		return fmt.Errorf("Codex account name is required")
	}
	if name == "json" {
		return fmt.Errorf("Codex account name %q is reserved", name)
	}
	if strings.HasPrefix(name, "backup-") {
		return fmt.Errorf("Codex account name prefix %q is reserved", "backup-")
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("Codex account name %q must use letters, numbers, dot, dash, underscore, plus, at, or colon", name)
	}
	return nil
}

func sanitizeCodexAccountName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '+' || r == '@' || r == ':' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	name := strings.Trim(b.String(), ".-_:@+")
	if err := validateCodexAccountName(name); err != nil {
		return ""
	}
	return name
}

func uniqueCodexAccountName(base string) string {
	base = sanitizeCodexAccountName(base)
	if base == "" {
		return ""
	}
	if _, err := os.Stat(codexAccountPath(base)); errors.Is(err, os.ErrNotExist) {
		return base
	}
	for i := 2; i < 1000; i++ {
		name := fmt.Sprintf("%s-%d", base, i)
		if _, err := os.Stat(codexAccountPath(name)); errors.Is(err, os.ErrNotExist) {
			return name
		}
	}
	return base + "-" + time.Now().Format("20060102-150405")
}

func listCodexAccounts(includeUsage bool) ([]codexAccount, error) {
	dir := codexHomeDir()
	currentPath := filepath.Join(dir, "auth.json")
	current, _ := os.ReadFile(currentPath)
	paths, err := filepath.Glob(filepath.Join(dir, "auth.*.json"))
	if err != nil {
		return nil, err
	}
	accounts := make([]codexAccount, 0, len(paths))
	for _, path := range paths {
		base := filepath.Base(path)
		name := strings.TrimSuffix(strings.TrimPrefix(base, "auth."), ".json")
		if name == "" || name == "json" {
			continue
		}
		if strings.HasPrefix(name, "backup-") {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		updated := "-"
		if st, err := os.Stat(path); err == nil {
			updated = st.ModTime().Format("01-02 15:04")
		}
		accounts = append(accounts, codexAccount{
			Name:    name,
			Path:    path,
			Email:   codexAuthEmail(content),
			Current: len(current) > 0 && bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(content)),
			Updated: updated,
		})
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		if accounts[i].Current != accounts[j].Current {
			return accounts[i].Current
		}
		return accounts[i].Name < accounts[j].Name
	})
	if includeUsage {
		enrichCodexAccountUsage(accounts)
	}
	return accounts, nil
}

func printCodexAccounts(accounts []codexAccount) error {
	width, _, _ := term.GetSize(int(os.Stdout.Fd()))
	if width <= 0 {
		width = 100
	}
	printCodexAccountRows(accounts, width, -1)
	return nil
}

func printCodexAccountRows(accounts []codexAccount, width, selected int) {
	inner := max(1, width-2)
	border := "+" + strings.Repeat("-", inner) + "+"
	fmt.Println(border)
	for idx, acc := range accounts {
		for _, line := range codexAccountRowLines(acc, idx+1, max(1, inner-2)) {
			text := "| " + padDisplay(line, max(1, inner-2)) + " |"
			if idx == selected {
				text = reverse(text)
			}
			fmt.Println(text)
		}
		fmt.Println(border)
	}
}

func codexAccountRowLines(acc codexAccount, index, width int) []string {
	current := " "
	if acc.Current {
		current = "*"
	}
	first := fmt.Sprintf("%s%d  %s", current, index, acc.Name)
	if acc.Email != "" {
		first += "  " + acc.Email
	}
	lines := hardWrapLine("", first, max(1, width))
	parts := []string{
		"updated:" + acc.Updated,
		"file:" + filepath.Base(acc.Path),
	}
	if usageParts := codexUsageParts(acc.Usage); len(usageParts) > 0 {
		parts = append(parts, usageParts...)
	}
	return append(lines, wrapLineParts("    ", parts, max(1, width))...)
}

func codexUsageParts(usage codexUsageSummary) []string {
	if usage.Error != "" {
		return []string{"usage:" + usage.Error}
	}
	var parts []string
	if usage.Plan != "" {
		parts = append(parts, "plan:"+usage.Plan)
	}
	if usage.Primary != "" || usage.Secondary != "" {
		value := strings.Trim(strings.Join(nonEmpty([]string{usage.Primary, usage.Secondary}), "/"), "/")
		if value != "" {
			parts = append(parts, "usage:"+value)
		}
	}
	if usage.Credits != "" {
		parts = append(parts, "credits:"+usage.Credits)
	}
	if usage.Coupons != "" {
		parts = append(parts, "coupons:"+usage.Coupons)
	}
	if usage.Updated != "" {
		parts = append(parts, "usage-updated:"+usage.Updated)
	}
	return parts
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func enrichCodexAccountUsage(accounts []codexAccount) {
	if len(accounts) == 0 || truthyEnv("AGEMUX_CODEX_USAGE_DISABLE") {
		return
	}
	enrichCodexAccountUsageWithProgress(accounts, nil)
}

func runCodexLoadingRefresh(accounts []codexAccount) {
	if len(accounts) == 0 || truthyEnv("AGEMUX_CODEX_USAGE_DISABLE") {
		return
	}
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		enrichCodexAccountUsage(accounts)
		return
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)
	fmt.Print("\033[?25l\033[?1049h")
	defer fmt.Print("\033[?1049l\033[?25h")

	done := make(chan struct{})
	var mu sync.Mutex
	active := map[int]string{}
	go func() {
		enrichCodexAccountUsageWithProgress(accounts, func(event string, acc codexAccount, idx int) {
			mu.Lock()
			defer mu.Unlock()
			if event == "start" {
				active[idx] = codexAccountDisplayName(acc)
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
			tuiLine(bold("Codex accounts") + " - refreshing usage " + spinner[frame%len(spinner)])
			tuiLine(dim("rate limits, credits, and reset credits refresh run in parallel"))
			tuiLine("")
			mu.Lock()
			var lines []string
			for idx, name := range active {
				lines = append(lines, fmt.Sprintf("%d  %s", idx, name))
			}
			mu.Unlock()
			sort.Strings(lines)
			if len(lines) == 0 {
				lines = append(lines, "waiting for workers...")
			}
			for _, line := range lines[:min(len(lines), height-5)] {
				tuiLine(clip(line, width-1))
			}
			frame++
		}
	}
}

func enrichCodexAccountUsageWithProgress(accounts []codexAccount, progress func(string, codexAccount, int)) {
	client := &http.Client{Timeout: 8 * time.Second}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range accounts {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if progress != nil {
				progress("start", accounts[i], i+1)
			}
			accounts[i].Usage = fetchCodexUsage(client, accounts[i])
			if progress != nil {
				progress("done", accounts[i], i+1)
			}
		}()
	}
	wg.Wait()
}

func codexAccountDisplayName(acc codexAccount) string {
	if acc.Email != "" {
		return acc.Email
	}
	return acc.Name
}

func fetchCodexUsage(client *http.Client, acc codexAccount) codexUsageSummary {
	content, err := os.ReadFile(acc.Path)
	if err != nil {
		return codexUsageSummary{Error: "unreadable"}
	}
	token := codexAuthAccessToken(content)
	if token == "" {
		return codexUsageSummary{}
	}
	if codexAccessTokenExpired(token, time.Now()) {
		return codexUsageSummary{Error: "token-expired"}
	}
	endpoint := envDefault("AGEMUX_CODEX_USAGE_URL", "https://chatgpt.com/backend-api/wham/usage")
	if !allowedCodexUsageEndpoint(endpoint) {
		return codexUsageSummary{Error: "bad-url"}
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return codexUsageSummary{Error: "bad-url"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agemux/0.1.7")
	resp, err := client.Do(req)
	if err != nil {
		return codexUsageSummary{Error: "fetch-failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return codexUsageSummary{Error: fmt.Sprintf("http-%d", resp.StatusCode)}
	}
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return codexUsageSummary{Error: "bad-json"}
	}
	return parseCodexUsage(payload, time.Now())
}

func parseCodexUsage(payload map[string]any, now time.Time) codexUsageSummary {
	rate, _ := payload["rate_limit"].(map[string]any)
	credits, _ := payload["credits"].(map[string]any)
	resetCredits, _ := payload["rate_limit_reset_credits"].(map[string]any)
	summary := codexUsageSummary{
		Plan:      stringField(payload, "plan_type"),
		Primary:   formatCodexRateWindow(mapField(rate, "primary_window")),
		Secondary: formatCodexRateWindow(mapField(rate, "secondary_window")),
		Credits:   formatCodexCredits(credits),
		Coupons:   formatNumberField(resetCredits, "available_count"),
		Updated:   formatShortAge(now),
	}
	if summary.Coupons == "" {
		summary.Coupons = "0"
	}
	return summary
}

func allowedCodexUsageEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" &&
		strings.EqualFold(parsed.Hostname(), "chatgpt.com") &&
		parsed.Path == "/backend-api/wham/usage"
}

func formatCodexRateWindow(window map[string]any) string {
	if len(window) == 0 {
		return ""
	}
	label := formatWindowSeconds(int64Field(window, "limit_window_seconds"))
	percent := formatNumberField(window, "used_percent")
	if percent == "" {
		return ""
	}
	if label == "" {
		return percent + "%"
	}
	return label + ":" + percent + "%"
}

func formatCodexCredits(credits map[string]any) string {
	if len(credits) == 0 {
		return ""
	}
	if boolField(credits, "unlimited") {
		return "unlimited"
	}
	if balance := stringField(credits, "balance"); balance != "" {
		return balance
	}
	if has, ok := credits["has_credits"].(bool); ok {
		if has {
			return "available"
		}
		return "none"
	}
	return ""
}

func formatWindowSeconds(seconds int64) string {
	switch {
	case seconds <= 0:
		return ""
	case seconds%86400 == 0:
		return fmt.Sprintf("%dd", seconds/86400)
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func formatShortAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("15:04")
}

func mapField(data map[string]any, key string) map[string]any {
	if data == nil {
		return nil
	}
	child, _ := data[key].(map[string]any)
	return child
}

func stringField(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return value
}

func boolField(data map[string]any, key string) bool {
	if data == nil {
		return false
	}
	value, _ := data[key].(bool)
	return value
}

func int64Field(data map[string]any, key string) int64 {
	if data == nil {
		return 0
	}
	switch typed := data[key].(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		v, _ := typed.Int64()
		return v
	default:
		return 0
	}
}

func numericField(data map[string]any, key string) (float64, bool) {
	if data == nil {
		return 0, false
	}
	switch typed := data[key].(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil
	default:
		return 0, false
	}
}

func formatNumberField(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	switch typed := data[key].(type) {
	case string:
		return typed
	case int:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%.1f", typed)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func codexAccountPicker(accounts []codexAccount) (codexAccountAction, error) {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return codexAccountAction{}, err
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)
	fmt.Print("\033[?25l\033[?1049h")
	defer fmt.Print("\033[?1049l\033[?25h")

	selected := 0
	for i, acc := range accounts {
		if acc.Current {
			selected = i + 1
			break
		}
	}
	buf := make([]byte, 16)
	for {
		drawCodexAccountPicker(accounts, selected)
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return codexAccountAction{}, err
		}
		key := string(buf[:n])
		rowCount := len(accounts) + 1
		switch {
		case key == "\x1b[A":
			selected = (selected - 1 + rowCount) % rowCount
		case key == "\x1b[B" || key == "j":
			selected = (selected + 1) % rowCount
		case key == "\r" || key == "\n":
			if selected == 0 {
				return codexAccountAction{Action: "new"}, nil
			}
			return codexAccountAction{Action: "change", Account: &accounts[selected-1]}, nil
		case key == "n":
			return codexAccountAction{Action: "new"}, nil
		case key == "l":
			if selected > 0 {
				return codexAccountAction{Action: "login", Account: &accounts[selected-1]}, nil
			}
		case key == "s":
			if selected > 0 {
				return codexAccountAction{Action: "status", Account: &accounts[selected-1]}, nil
			}
		case key == "r":
			enrichCodexAccountUsage(accounts)
		case key == "d" || key == "k" || key == "x":
			if selected == 0 {
				continue
			}
			accountIndex := selected - 1
			if confirm(fmt.Sprintf("Delete %s from Codex accounts? y/N", codexAccountLabel(accounts[accountIndex]))) {
				if _, err := deleteCodexAccount(accounts[accountIndex]); err != nil {
					return codexAccountAction{}, err
				}
				var reloadErr error
				accounts, reloadErr = listCodexAccounts(false)
				if reloadErr != nil {
					return codexAccountAction{}, reloadErr
				}
				if selected > len(accounts) {
					selected = len(accounts)
				}
			}
		case key == "q" || key == "\x1b":
			return codexAccountAction{}, nil
		}
	}
}

func drawCodexAccountPicker(accounts []codexAccount, selected int) {
	width, height, _ := term.GetSize(int(os.Stdout.Fd()))
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 24
	}
	fmt.Print("\033[H\033[2J")
	tuiLine(bold(clip("agemux", width-1)) + clip(" - Codex accounts", max(0, width-1-len("agemux"))))
	tuiLine(dim(clip("Up/Down move  Enter select  n new  l login  s status  r refresh  d delete  q/Esc back", width-1)))
	tuiLine(strings.Repeat("-", min(width-1, 1000)))
	visible := max(1, (height-5)/4)
	offset := 0
	if selected >= visible {
		offset = selected - visible + 1
	}
	rowCount := len(accounts) + 1
	for idx := offset; idx < min(rowCount, offset+visible); idx++ {
		var lines []string
		if idx == 0 {
			lines = renderCodexAddAccountTUILines(idx == selected, width-1)
		} else {
			lines = renderCodexAccountTUILines(accounts[idx-1], idx, idx == selected, width-1)
		}
		for _, line := range lines {
			tuiLine(line)
		}
	}
	fmt.Printf("\033[%d;1H%s", height, dim(clip(fmt.Sprintf("%d Codex account(s)", len(accounts)), width-1)))
}

func renderCodexAddAccountTUILines(selected bool, width int) []string {
	lines := []string{
		"+ Add Codex account",
		"    Sign in with codex login and save it as a selectable auth file",
	}
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

func renderCodexAccountTUILines(acc codexAccount, index int, selected bool, width int) []string {
	lines := codexAccountRowLines(acc, index, max(1, width-2))
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

func wrapLineParts(prefix string, parts []string, width int) []string {
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
			lines = append(lines, hardWrapLine(prefix, part, width)...)
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

func hardWrapLine(prefix, text string, width int) []string {
	bodyWidth := max(1, width-displayWidth(prefix))
	runes := []rune(text)
	var lines []string
	for len(runes) > 0 {
		chunk := clipDisplayWidth(string(runes), bodyWidth)
		if chunk == "" {
			break
		}
		lines = append(lines, prefix+chunk)
		runes = runes[len([]rune(chunk)):]
	}
	if len(lines) == 0 {
		return []string{prefix}
	}
	return lines
}

func switchCodexAccount(acc codexAccount) error {
	content, err := os.ReadFile(acc.Path)
	if err != nil {
		return err
	}
	dir := codexHomeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	target := filepath.Join(dir, "auth.json")
	if err := backupUntrackedActiveCodexAuth(dir, target, content); err != nil {
		return err
	}
	return writeActiveCodexAuth(content)
}

func writeActiveCodexAuth(content []byte) error {
	dir := codexHomeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "auth.json"), content, 0600)
}

func writeFileAtomic(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.%d.tmp", filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(tmp, content, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(path, perm)
}

func backupUntrackedActiveCodexAuth(dir, target string, replacement []byte) error {
	current, err := os.ReadFile(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	current = bytes.TrimSpace(current)
	if len(current) == 0 || bytes.Equal(current, bytes.TrimSpace(replacement)) {
		return nil
	}
	paths, err := filepath.Glob(filepath.Join(dir, "auth.*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err == nil && bytes.Equal(bytes.TrimSpace(content), current) {
			return nil
		}
	}
	backup := filepath.Join(dir, fmt.Sprintf("auth.backup-%s-%d.json", time.Now().Format("20060102-150405"), time.Now().UnixNano()))
	if err := os.WriteFile(backup, append([]byte(nil), current...), 0600); err != nil {
		return err
	}
	return os.Chmod(backup, 0600)
}

func codexAuthEmail(content []byte) string {
	var data map[string]any
	if err := json.Unmarshal(content, &data); err != nil {
		return ""
	}
	tokens, _ := data["tokens"].(map[string]any)
	idToken, _ := tokens["id_token"].(string)
	if idToken == "" {
		return findEmail(data)
	}
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return findEmail(data)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return findEmail(data)
	}
	var claims any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return findEmail(data)
	}
	if email := findEmail(claims); email != "" {
		return email
	}
	return findEmail(data)
}

func codexAuthAccessToken(content []byte) string {
	var data map[string]any
	if err := json.Unmarshal(content, &data); err != nil {
		return ""
	}
	tokens, _ := data["tokens"].(map[string]any)
	for _, key := range []string{"access_token", "accessToken"} {
		if value, _ := tokens[key].(string); value != "" {
			return value
		}
	}
	return ""
}

func codexAccessTokenExpired(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	exp, ok := numericField(claims, "exp")
	if !ok || exp <= 0 {
		return false
	}
	return now.Unix() >= int64(exp)
}

func findEmail(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if email, _ := typed["email"].(string); email != "" {
			return email
		}
		for _, child := range typed {
			if email := findEmail(child); email != "" {
				return email
			}
		}
	case []any:
		for _, child := range typed {
			if email := findEmail(child); email != "" {
				return email
			}
		}
	}
	return ""
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func killSession(name string) error {
	if err := ensureName(name); err != nil {
		return err
	}
	cmd := exec.Command(shpoolBin, "kill", "--", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s kill failed: %s", shpoolBin, strings.TrimSpace(string(out)))
	}
	return withMetaLock(func(meta metadata) error {
		delete(meta, name)
		return saveMetaUnlocked(meta)
	})
}

func menuItems() ([]menuItem, error) {
	items := []menuItem{
		{Type: "codex-resume", Label: "Codex resume picker"},
		{Type: "claude-resume", Label: "Claude resume picker"},
		{Type: "codex-fresh", Label: "New Codex"},
		{Type: "claude-fresh", Label: "New Claude"},
		{Type: "codex-accounts", Label: "Codex accounts"},
		{Type: "claude-accounts", Label: "Claude accounts"},
	}
	sessions, err := agemuxSessions()
	if err != nil {
		return nil, err
	}
	for _, sess := range sessions {
		meta, _ := sess["meta"].(map[string]any)
		name := stringValue(sess["name"])
		title := stringValue(meta["title"])
		if title == "" {
			title = name
		}
		kind := stringValue(meta["kind"])
		provider := stringValue(meta["provider"])
		if provider == "" {
			provider, _ = splitKind(kind)
		}
		detail := fmt.Sprintf("%s  %s  %s",
			providerLabel(provider),
			formatTime(int64Value(sess["started_at_unix_ms"])),
			stringValue(meta["root"]),
		)
		status := statusLabel(stringValue(sess["status"]))
		if status != "" {
			title = status + "  " + title
		}
		items = append(items, menuItem{Type: "session", Name: name, Label: title, Detail: detail})
	}
	return items, nil
}

func statusLabel(status string) string {
	switch strings.ToLower(status) {
	case "attached":
		return "Attached"
	case "detached":
		return "Detached"
	default:
		return ""
	}
}

func providerLabel(provider string) string {
	switch provider {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	default:
		if provider == "" {
			return "Agent"
		}
		return provider
	}
}

func formatTime(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Format("01-02 15:04")
}

func interactive() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) || os.Getenv("TERM") == "dumb" {
		action, value, err := plainMenu()
		if err != nil || action == "" {
			return err
		}
		return runAction(action, value)
	}
	for {
		action, value, err := tuiMenu()
		if err != nil || action == "" {
			return err
		}
		if action == "kill" {
			if err := killSession(value); err != nil {
				return err
			}
			continue
		}
		if action == "accounts" {
			if err := runAction(action, value); err != nil {
				return err
			}
			continue
		}
		return runAction(action, value)
	}
}

func runAction(action, value string) error {
	switch action {
	case "new":
		return create(value)
	case "accounts":
		if value == "codex-accounts" {
			return codexAccountsInteractive()
		}
		if value == "claude-accounts" {
			return claudeaccounts.RunMain([]string{"agemux claude-accounts"})
		}
		return fmt.Errorf("unknown account action: %s", value)
	case "attach":
		return execAttach(value, "", false)
	case "kill":
		return killSession(value)
	default:
		return nil
	}
}

func plainMenu() (string, string, error) {
	items, err := menuItems()
	if err != nil {
		return "", "", err
	}
	fmt.Println()
	fmt.Println("agemux - persistent Codex and Claude sessions via shpool")
	fmt.Println()
	for i, item := range items {
		fmt.Printf("%d. %s\n", i+1, item.Label)
	}
	fmt.Print("\nselect> ")
	var choice string
	if _, err := fmt.Scanln(&choice); err != nil {
		return "", "", err
	}
	if choice == "" || choice == "q" || choice == "quit" || choice == "exit" {
		return "", "", nil
	}
	var idx int
	if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(items) {
		return "", "", fmt.Errorf("selection out of range")
	}
	item := items[idx-1]
	if item.Type == "session" {
		return "attach", item.Name, nil
	}
	if strings.HasSuffix(item.Type, "-accounts") {
		return "accounts", item.Type, nil
	}
	return "new", item.Type, nil
}

func tuiMenu() (string, string, error) {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return "", "", err
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)
	stdinFD := int(os.Stdin.Fd())
	if err := syscall.SetNonblock(stdinFD, true); err != nil {
		return "", "", err
	}
	defer syscall.SetNonblock(stdinFD, false)
	fmt.Print("\033[?25l\033[?1049h")
	defer fmt.Print("\033[?1049l\033[?25h")

	selected := 0
	lastActionCol := 0
	reader := make([]byte, 16)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	lastWidth, lastHeight := 0, 0
	dirty := true
	for {
		items, err := menuItems()
		if err != nil {
			return "", "", err
		}
		if len(items) == 0 {
			return "", "", nil
		}
		if selected >= len(items) {
			selected = len(items) - 1
		}
		if selected < actionMenuCount(len(items)) {
			lastActionCol = selected % actionMenuCols
		}
		width, height, _ := term.GetSize(int(os.Stdout.Fd()))
		if width != lastWidth || height != lastHeight {
			lastWidth, lastHeight = width, height
			dirty = true
		}
		if dirty {
			drawMenu(items, selected)
			dirty = false
		}
		n, err := os.Stdin.Read(reader)
		if err != nil && !errors.Is(err, syscall.EAGAIN) {
			return "", "", err
		}
		if n == 0 {
			select {
			case <-winch:
				dirty = true
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		key := string(reader[:n])
		switch {
		case key == "\x1b[A":
			selected = moveSelectionUp(selected, len(items), lastActionCol)
			dirty = true
		case key == "\x1b[B" || key == "j":
			selected = moveSelectionDown(selected, len(items), lastActionCol)
			dirty = true
		case key == "\x1b[D":
			selected = moveSelectionLeft(selected, len(items))
			dirty = true
		case key == "\x1b[C":
			selected = moveSelectionRight(selected, len(items))
			dirty = true
		case key == "\r" || key == "\n":
			item := items[selected]
			if item.Type == "session" {
				return "attach", item.Name, nil
			}
			if strings.HasSuffix(item.Type, "-accounts") {
				return "accounts", item.Type, nil
			}
			return "new", item.Type, nil
		case key == "c":
			return "new", "codex-resume", nil
		case key == "C":
			return "new", "codex-fresh", nil
		case key == "l":
			return "new", "claude-resume", nil
		case key == "L":
			return "new", "claude-fresh", nil
		case key == "k" || key == "x":
			item := items[selected]
			if item.Type == "session" && confirm(fmt.Sprintf("Kill %s (%s)? y/N", item.Label, item.Name)) {
				return "kill", item.Name, nil
			}
			dirty = true
		case key == "q" || key == "\x1b":
			return "", "", nil
		}
	}
}

const (
	actionMenuSize = 6
	actionMenuCols = 2
)

func actionMenuCount(total int) int {
	return min(actionMenuSize, total)
}

func moveSelectionUp(selected, total, lastActionCol int) int {
	actionCount := actionMenuCount(total)
	if total == 0 {
		return 0
	}
	if selected < actionCount {
		if selected >= actionMenuCols {
			return selected - actionMenuCols
		}
		if total > actionCount {
			return total - 1
		}
		return bottomActionIndex(actionCount, lastActionCol)
	}
	if selected == actionCount {
		return bottomActionIndex(actionCount, lastActionCol)
	}
	return selected - 1
}

func moveSelectionDown(selected, total, lastActionCol int) int {
	actionCount := actionMenuCount(total)
	if total == 0 {
		return 0
	}
	if selected < actionCount {
		next := selected + actionMenuCols
		if next < actionCount {
			return next
		}
		if total > actionCount {
			return actionCount
		}
		return lastActionCol
	}
	if selected >= total-1 {
		return lastActionCol
	}
	return selected + 1
}

func bottomActionIndex(actionCount, col int) int {
	if actionCount == 0 {
		return 0
	}
	idx := ((actionCount - 1) / actionMenuCols * actionMenuCols) + col
	if idx >= actionCount {
		idx -= actionMenuCols
	}
	if idx < 0 {
		return 0
	}
	return idx
}

func moveSelectionLeft(selected, total int) int {
	actionCount := actionMenuCount(total)
	if selected >= actionCount || selected%actionMenuCols == 0 {
		return selected
	}
	return selected - 1
}

func moveSelectionRight(selected, total int) int {
	actionCount := actionMenuCount(total)
	if selected >= actionCount || selected%actionMenuCols == actionMenuCols-1 || selected+1 >= actionCount {
		return selected
	}
	return selected + 1
}

func drawMenu(items []menuItem, selected int) {
	width, height, _ := term.GetSize(int(os.Stdout.Fd()))
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	fmt.Print("\033[H\033[2J")
	tuiLine(bold(clip("agemux", width-1)) + clip(" - persistent Codex and Claude sessions via shpool", max(0, width-1-len("agemux"))))
	tuiLine(dim(clip("Arrows move  Enter open  c Codex  C new Codex  l Claude  L new Claude  k kill  q/Esc quit", width-1)))
	tuiLine(strings.Repeat("-", min(width-1, 1000)))
	drawActionGrid(items, selected, width)
	tuiLine("")
	tuiLine(dim(clip("Sessions", width-1)))
	drawSessionList(items, selected, width, height)
	live := 0
	for _, item := range items {
		if item.Type == "session" {
			live++
		}
	}
	fmt.Printf("\033[%d;1H%s", height, dim(clip(fmt.Sprintf("%d live session(s)", live), width-1)))
}

func drawActionGrid(items []menuItem, selected, width int) {
	actionCount := actionMenuCount(len(items))
	if actionCount == 0 {
		return
	}
	available := max(1, width-1)
	gap := 2
	if available < 24 {
		gap = 1
	}
	colWidth := max(1, (available-gap)/actionMenuCols)
	rows := (actionCount + actionMenuCols - 1) / actionMenuCols
	for row := 0; row < rows; row++ {
		left := row * actionMenuCols
		right := left + 1
		if left >= actionCount {
			break
		}
		line := renderActionCell(items[left], left == selected, colWidth)
		if right < actionCount {
			line += strings.Repeat(" ", gap) + renderActionCell(items[right], right == selected, colWidth)
		}
		tuiLine(line)
	}
}

func renderActionCell(item menuItem, selected bool, width int) string {
	line := "  + " + item.Label
	if selected {
		line = "> + " + item.Label
	}
	line = padDisplay(clip(line, width), width)
	if selected {
		return reverse(line)
	}
	return line
}

func drawSessionList(items []menuItem, selected, width, height int) {
	actionCount := actionMenuCount(len(items))
	sessions := items[actionCount:]
	if len(sessions) == 0 {
		tuiLine(dim(clip("  No live sessions", width-1)))
		return
	}
	headerLines := 8
	sessionRows := max(1, (height-headerLines-1)/2)
	sessionSelected := selected - actionCount
	offset := 0
	if sessionSelected >= sessionRows {
		offset = sessionSelected - sessionRows + 1
	}
	for i, item := range sessions[offset:min(len(sessions), offset+sessionRows)] {
		idx := actionCount + offset + i
		line := "  " + item.Label
		if idx == selected {
			line = "> " + item.Label
		}
		line = clip(line, width-1)
		if idx == selected {
			line = reverse(padDisplay(line, width-1))
		}
		tuiLine(line)
		if item.Detail != "" {
			tuiLine(dim(clip("    "+item.Detail, width-1)))
		}
	}
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
	fmt.Printf("\033[%d;1H%s", height, reverse(clip(prompt, width-1)))
	buf := []byte{0}
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) {
				time.Sleep(30 * time.Millisecond)
				continue
			}
			return false
		}
		if n == 0 {
			time.Sleep(30 * time.Millisecond)
			continue
		}
		switch buf[0] {
		case 'y', 'Y':
			return true
		case 'n', 'N', '\r', '\n', 27:
			return false
		}
	}
}

func bold(s string) string    { return "\033[1m" + s + "\033[0m" }
func dim(s string) string     { return "\033[2m" + s + "\033[0m" }
func reverse(s string) string { return "\033[7m" + s + "\033[0m" }

func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(s) <= width {
		return s
	}
	if width <= 3 {
		return clipDisplayWidth(s, width)
	}
	return clipDisplayWidth(s, width-3) + "..."
}

func clipDisplayWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runeWidth(r)
		if used+rw > width {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String()
}

func padDisplay(s string, width int) string {
	padding := width - displayWidth(s)
	if padding <= 0 {
		return s
	}
	return s + strings.Repeat(" ", padding)
}

func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		width += runeWidth(r)
	}
	return width
}

func runeWidth(r rune) int {
	if r == 0 || r < 32 || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f ||
		r == 0x2329 || r == 0x232a ||
		r >= 0x2e80 && r <= 0xa4cf ||
		r >= 0xac00 && r <= 0xd7a3 ||
		r >= 0xf900 && r <= 0xfaff ||
		r >= 0xfe10 && r <= 0xfe19 ||
		r >= 0xfe30 && r <= 0xfe6f ||
		r >= 0xff00 && r <= 0xff60 ||
		r >= 0xffe0 && r <= 0xffe6 ||
		r >= 0x1f300 && r <= 0x1faff) {
		return 2
	}
	return 1
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

type titleParser struct {
	buffer    []byte
	lastTitle string
	callback  func(string)
}

func (p *titleParser) feed(data []byte) {
	p.buffer = append(p.buffer, data...)
	for {
		match := titleRE.FindSubmatchIndex(p.buffer)
		if match == nil {
			if len(p.buffer) > 8192 {
				p.buffer = p.buffer[len(p.buffer)-8192:]
			}
			return
		}
		if len(match) < 4 {
			p.buffer = p.buffer[match[1]:]
			continue
		}
		title := string(p.buffer[match[2]:match[3]])
		title = cleanTitle(title)
		if title != "" && title != p.lastTitle {
			p.lastTitle = title
			p.callback(title)
		}
		p.buffer = p.buffer[match[1]:]
	}
}

func runAgentSession(name, kind, root string) error {
	if err := ensureName(name); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(expandPath(root))
	if err == nil {
		root = absRoot
	}
	args, err := agentArgs(kind, root)
	if err != nil {
		return err
	}
	if err := registerSession(name, kind, root); err != nil {
		return err
	}
	_ = updateMeta(name, map[string]any{"last_started_at": time.Now().UTC().Format(time.RFC3339Nano)})
	terminalTitle(sessionTitle(name))

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = root
	ptmx, err := pty.StartWithSize(cmd, currentWindowSize())
	if err != nil {
		return err
	}
	defer ptmx.Close()

	var oldState *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, _ = term.MakeRaw(int(os.Stdin.Fd()))
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			_ = pty.Setsize(ptmx, currentWindowSize())
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGWINCH)
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if term.IsTerminal(int(os.Stdin.Fd())) {
		go func() {
			_, _ = io.Copy(ptmx, os.Stdin)
			cancel()
		}()
	}

	parser := &titleParser{callback: func(title string) {
		_ = updateMeta(name, map[string]any{
			"title":            title,
			"title_updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
	}}
	buf := make([]byte, 4096)
	exitCode := 0
relayLoop:
	for {
		n, readErr := ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			parser.feed(chunk)
			_, _ = os.Stdout.Write(chunk)
		}
		if readErr != nil {
			break
		}
		select {
		case <-ctx.Done():
			break relayLoop
		default:
		}
	}
	err = cmd.Wait()
	var exitErr *exec.ExitError
	if err != nil && errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		return err
	}
	if exitCode != 0 {
		return exitCodeError(exitCode)
	}
	return nil
}

type exitCodeError int

func (e exitCodeError) Error() string {
	return fmt.Sprintf("agent exited with status %d", int(e))
}

func currentWindowSize() *pty.Winsize {
	size, err := pty.GetsizeFull(os.Stdin)
	if err == nil {
		return size
	}
	return &pty.Winsize{Rows: 24, Cols: 80}
}
