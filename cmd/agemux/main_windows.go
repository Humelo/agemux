package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Humelo/agemux/internal/claudeaccounts"
)

type windowsCodexAccount struct {
	Name    string
	Path    string
	Email   string
	Current bool
	Updated string
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "claude-accounts" {
		if err := claudeaccounts.RunMain(append([]string{"agemux claude-accounts"}, os.Args[2:]...)); err != nil {
			if code, ok := err.(claudeaccounts.ExitCodeError); ok {
				os.Exit(int(code))
			}
			fmt.Fprintln(os.Stderr, "Claude accounts:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "codex-accounts" {
		if err := runWindowsCodexAccounts(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "Codex accounts:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help") {
		fmt.Println(`Usage:
  agemux                  interactive session picker
  agemux codex            new shpool session running Codex resume picker
  agemux codex-new        new shpool session running fresh Codex
  agemux claude           new shpool session running Claude resume picker
  agemux claude-new       new shpool session running fresh Claude
  agemux codex-accounts   list or switch the active Codex auth file
  agemux claude-accounts  open the Claude account switcher
  agemux list             list live agemux shpool sessions
  agemux attach NAME      attach to a live session
  agemux kill NAME        kill a session`)
		fmt.Println()
		fmt.Println("Native Windows is not supported for agemux sessions. Use WSL, Linux, or macOS.")
		return
	}
	fmt.Fprintln(os.Stderr, "agemux requires POSIX PTY support and shpool. Use WSL, Linux, or macOS.")
	os.Exit(1)
}

func runWindowsCodexAccounts(args []string) error {
	accounts, err := listWindowsCodexAccounts()
	if err != nil {
		return err
	}
	command := "list"
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "", "list":
		return printWindowsCodexAccounts(accounts)
	case "current":
		for _, acc := range accounts {
			if acc.Current {
				fmt.Printf("current Codex account: %s\n", windowsCodexAccountLabel(acc))
				return nil
			}
		}
		fmt.Println("no current Codex account")
		return nil
	case "change":
		if len(args) < 2 {
			return printWindowsCodexAccounts(accounts)
		}
		acc, err := resolveWindowsCodexAccount(accounts, args[1])
		if err != nil {
			return err
		}
		if err := switchWindowsCodexAccount(acc); err != nil {
			return err
		}
		fmt.Printf("current Codex account: %s\n", windowsCodexAccountLabel(acc))
		return nil
	default:
		return fmt.Errorf("usage: agemux codex-accounts [list|current|change SELECTOR]")
	}
}

func windowsCodexHomeDir() string {
	if value := os.Getenv("CODEX_HOME"); value != "" {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex")
	}
	return ".codex"
}

func listWindowsCodexAccounts() ([]windowsCodexAccount, error) {
	dir := windowsCodexHomeDir()
	current, _ := os.ReadFile(filepath.Join(dir, "auth.json"))
	paths, err := filepath.Glob(filepath.Join(dir, "auth.*.json"))
	if err != nil {
		return nil, err
	}
	accounts := make([]windowsCodexAccount, 0, len(paths))
	for _, path := range paths {
		base := filepath.Base(path)
		name := strings.TrimSuffix(strings.TrimPrefix(base, "auth."), ".json")
		if name == "" || name == "json" || strings.HasPrefix(name, "backup-") {
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
		accounts = append(accounts, windowsCodexAccount{
			Name:    name,
			Path:    path,
			Email:   windowsCodexAuthEmail(content),
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
	return accounts, nil
}

func printWindowsCodexAccounts(accounts []windowsCodexAccount) error {
	if len(accounts) == 0 {
		fmt.Printf("No Codex account files found in %s\n", windowsCodexHomeDir())
		fmt.Println("Expected files like auth.tools.json next to auth.json.")
		return nil
	}
	for i, acc := range accounts {
		marker := " "
		if acc.Current {
			marker = "*"
		}
		fmt.Printf("%s%d  %s  updated:%s  file:%s\n", marker, i+1, windowsCodexAccountLabel(acc), acc.Updated, filepath.Base(acc.Path))
	}
	return nil
}

func resolveWindowsCodexAccount(accounts []windowsCodexAccount, selector string) (windowsCodexAccount, error) {
	if selector == "" {
		return windowsCodexAccount{}, fmt.Errorf("missing selector")
	}
	var matches []windowsCodexAccount
	for i, acc := range accounts {
		if fmt.Sprint(i+1) == selector || acc.Name == selector || strings.EqualFold(acc.Email, selector) {
			return acc, nil
		}
		text := strings.ToLower(acc.Name + " " + acc.Email)
		if strings.Contains(text, strings.ToLower(selector)) {
			matches = append(matches, acc)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return windowsCodexAccount{}, fmt.Errorf("ambiguous selector %q", selector)
	}
	return windowsCodexAccount{}, fmt.Errorf("no Codex account matches %q", selector)
}

func switchWindowsCodexAccount(acc windowsCodexAccount) error {
	content, err := os.ReadFile(acc.Path)
	if err != nil {
		return err
	}
	dir := windowsCodexHomeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".auth.json.%d.%d.tmp", os.Getpid(), time.Now().UnixNano()))
	if err := os.WriteFile(tmp, content, 0600); err != nil {
		return err
	}
	target := filepath.Join(dir, "auth.json")
	if err := backupUntrackedWindowsCodexAuth(dir, target, content); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(target, 0600)
}

func backupUntrackedWindowsCodexAuth(dir, target string, replacement []byte) error {
	current, err := os.ReadFile(target)
	if os.IsNotExist(err) {
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
	if err := os.WriteFile(backup, current, 0600); err != nil {
		return err
	}
	return os.Chmod(backup, 0600)
}

func windowsCodexAccountLabel(acc windowsCodexAccount) string {
	if acc.Email != "" {
		return acc.Name + " <" + acc.Email + ">"
	}
	return acc.Name
}

func windowsCodexAuthEmail(content []byte) string {
	var data map[string]any
	if err := json.Unmarshal(content, &data); err != nil {
		return ""
	}
	tokens, _ := data["tokens"].(map[string]any)
	idToken, _ := tokens["id_token"].(string)
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	email, _ := claims["email"].(string)
	return email
}
