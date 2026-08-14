//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Humelo/agemux/internal/terminalstate"
	"github.com/Humelo/agemux/internal/termkey"
	"github.com/gofrs/flock"
	"golang.org/x/term"
)

type grokAccount struct {
	Name    string
	Path    string
	Email   string
	Current bool
	Updated string
}

type grokAccountAction struct {
	Action  string
	Account *grokAccount
}

type grokAccountState struct {
	Name string `json:"name"`
}

func grokAccountsCommand(args []string) error {
	if len(args) == 0 {
		return grokAccountsInteractive()
	}
	switch args[0] {
	case "current":
		accounts, err := listGrokAccounts()
		if err != nil {
			return err
		}
		acc := currentGrokAccount(accounts)
		if acc == nil {
			fmt.Println("no current Grok account")
			return nil
		}
		fmt.Printf("current Grok account: %s\n", grokAccountLabel(*acc))
		return nil
	case "new":
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		return commandNewGrokAccount(name)
	case "change", "select", "use":
		accounts, err := listGrokAccounts()
		if err != nil {
			return err
		}
		acc, err := resolveGrokAccountSelector(accounts, args[1:])
		if err != nil {
			return err
		}
		if acc == nil {
			return fmt.Errorf("no Grok account configured")
		}
		if err := switchGrokAccount(*acc); err != nil {
			return err
		}
		fmt.Printf("current Grok account: %s\n", grokAccountLabel(*acc))
		return nil
	case "delete", "remove", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: agemux grok-accounts delete SELECTOR")
		}
		accounts, err := listGrokAccounts()
		if err != nil {
			return err
		}
		acc, err := resolveGrokAccountSelector(accounts, args[1:])
		if err != nil {
			return err
		}
		if acc == nil {
			return fmt.Errorf("no Grok account configured")
		}
		return deleteGrokAccountWithMessage(*acc)
	case "login":
		accounts, err := listGrokAccounts()
		if err != nil {
			return err
		}
		acc, err := resolveGrokAccountSelector(accounts, args[1:])
		if err != nil {
			return err
		}
		if acc == nil {
			return commandNewGrokAccount("")
		}
		return reloginGrokAccount(*acc)
	case "status":
		accounts, err := listGrokAccounts()
		if err != nil {
			return err
		}
		acc, err := resolveGrokAccountSelector(accounts, args[1:])
		if err != nil {
			return err
		}
		if acc == nil {
			return fmt.Errorf("no Grok account configured")
		}
		printGrokAccountStatus(*acc)
		return nil
	case "list":
		accounts, err := listGrokAccounts()
		if err != nil {
			return err
		}
		return printGrokAccounts(accounts)
	default:
		return fmt.Errorf("usage: agemux grok-accounts [list|current|change SELECTOR|new [name]|login [selector]|status [selector]|delete SELECTOR]")
	}
}

func grokAccountsInteractive() error {
	accounts, err := listGrokAccounts()
	if err != nil {
		return err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) || os.Getenv("TERM") == "dumb" {
		if len(accounts) == 0 {
			fmt.Printf("No Grok account files found in %s\n", grokHomeDir())
			fmt.Println("Run `agemux grok-accounts new` to add one.")
			return nil
		}
		return printGrokAccounts(accounts)
	}
	action, err := grokAccountPickerWithScreen(accounts)
	if err != nil || action.Action == "" {
		return err
	}
	if action.Action == "new" {
		return commandNewGrokAccount("")
	}
	if action.Account == nil {
		return nil
	}
	switch action.Action {
	case "change":
		if err := switchGrokAccount(*action.Account); err != nil {
			return err
		}
		fmt.Printf("current Grok account: %s\n", grokAccountLabel(*action.Account))
	case "login":
		return reloginGrokAccount(*action.Account)
	case "status":
		printGrokAccountStatus(*action.Account)
	}
	return nil
}

func grokAccountPickerWithScreen(accounts []grokAccount) (grokAccountAction, error) {
	screen, err := terminalstate.BeginScreen(os.Stdin, os.Stdout)
	if err != nil {
		return grokAccountAction{}, err
	}
	defer screen.Close()
	return grokAccountPicker(accounts, termkey.NewReader(os.Stdin))
}

func grokAccountLabel(acc grokAccount) string {
	label := acc.Name
	if acc.Email != "" {
		label += " <" + acc.Email + ">"
	}
	return label
}

func currentGrokAccount(accounts []grokAccount) *grokAccount {
	for i := range accounts {
		if accounts[i].Current {
			return &accounts[i]
		}
	}
	return nil
}

func resolveGrokAccountSelector(accounts []grokAccount, selector []string) (*grokAccount, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	query := normalizeSearch(strings.Join(selector, " "))
	if query == "" {
		return currentGrokAccount(accounts), nil
	}
	if len(selector) == 1 {
		if idx, ok := selectorIndex(selector[0]); ok {
			if idx < 0 || idx >= len(accounts) {
				return nil, fmt.Errorf("Grok account index out of range: %s", selector[0])
			}
			return &accounts[idx], nil
		}
	}
	matches := matchGrokAccounts(accounts, query)
	if len(matches) == 1 {
		return &accounts[matches[0]], nil
	}
	if len(matches) > 1 {
		var lines []string
		for _, idx := range matches {
			lines = append(lines, fmt.Sprintf("  %d  %s", idx+1, grokAccountLabel(accounts[idx])))
		}
		return nil, fmt.Errorf("ambiguous Grok account selector:\n%s", strings.Join(lines, "\n"))
	}
	return nil, fmt.Errorf("no Grok account matches %q", strings.Join(selector, " "))
}

func matchGrokAccounts(accounts []grokAccount, query string) []int {
	var matches []int
	for i, acc := range accounts {
		text := normalizeSearch(strings.Join([]string{strconv.Itoa(i + 1), acc.Name, acc.Email, filepath.Base(acc.Path)}, " "))
		if text == query || strings.HasPrefix(text, query) || strings.Contains(text, query) {
			matches = append(matches, i)
		}
	}
	return matches
}

func printGrokAccountStatus(acc grokAccount) {
	fmt.Printf("name: %s\n", acc.Name)
	if acc.Email != "" {
		fmt.Printf("email: %s\n", acc.Email)
	}
	fmt.Printf("path: %s\n", acc.Path)
	if acc.Current {
		fmt.Println("current: yes")
	} else {
		fmt.Println("current: no")
	}
}

func reloginGrokAccount(acc grokAccount) error {
	tempDir, err := os.MkdirTemp("", "agemux-grok-login-*")
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
	fmt.Printf("starting Grok login for %s...\n", grokAccountLabel(acc))
	cmd := exec.Command(grokBin, "login")
	cmd.Env = upsertEnv(os.Environ(), "GROK_HOME", tempDir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := runForegroundCommand(cmd); err != nil {
		return err
	}
	updated, err := os.ReadFile(filepath.Join(tempDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("Grok login did not create auth.json: %w", err)
	}
	if len(bytes.TrimSpace(updated)) == 0 {
		return fmt.Errorf("Grok login created an empty auth.json")
	}
	if err := withGrokAccountsLock(func() error {
		if err := writeFileAtomic(acc.Path, updated, 0600); err != nil {
			return err
		}
		if acc.Current {
			if err := writeActiveGrokAuth(updated); err != nil {
				return err
			}
			return saveGrokAccountState(acc.Name)
		}
		return nil
	}); err != nil {
		return err
	}
	acc.Email = grokAuthEmail(updated)
	fmt.Printf("updated Grok account: %s\n", grokAccountLabel(acc))
	return nil
}

func deleteGrokAccountWithMessage(acc grokAccount) error {
	next, err := deleteGrokAccount(acc)
	if err != nil {
		return err
	}
	fmt.Printf("deleted Grok account: %s\n", grokAccountLabel(acc))
	if next != nil {
		fmt.Printf("current Grok account: %s\n", grokAccountLabel(*next))
	} else {
		fmt.Println("no current Grok account")
	}
	return nil
}

func deleteGrokAccount(acc grokAccount) (*grokAccount, error) {
	wasCurrent := acc.Current
	if err := os.Remove(acc.Path); err != nil {
		return nil, err
	}
	if !wasCurrent {
		return currentGrokAccountAfterReload()
	}
	remaining, err := listGrokAccounts()
	if err != nil {
		return nil, err
	}
	if len(remaining) > 0 {
		next := remaining[0]
		nextContent, err := os.ReadFile(next.Path)
		if err != nil {
			return nil, err
		}
		if err := writeActiveGrokAuth(nextContent); err != nil {
			return nil, err
		}
		if err := saveGrokAccountState(next.Name); err != nil {
			return nil, err
		}
		next.Current = true
		return &next, nil
	}
	activePath := filepath.Join(grokHomeDir(), "auth.json")
	if err := os.Remove(activePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Remove(grokAccountStatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return nil, nil
}

func currentGrokAccountAfterReload() (*grokAccount, error) {
	accounts, err := listGrokAccounts()
	if err != nil {
		return nil, err
	}
	return currentGrokAccount(accounts), nil
}

func commandNewGrokAccount(name string) error {
	name = strings.TrimSpace(name)
	if name != "" {
		if err := validateGrokAccountName(name); err != nil {
			return err
		}
		if _, err := os.Stat(grokAccountPath(name)); err == nil {
			return fmt.Errorf("Grok account %q already exists", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	tempDir, err := os.MkdirTemp("", "agemux-grok-login-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	fmt.Println("starting Grok login for the new account...")
	cmd := exec.Command(grokBin, "login")
	cmd.Env = upsertEnv(os.Environ(), "GROK_HOME", tempDir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := runForegroundCommand(cmd); err != nil {
		return err
	}

	content, err := os.ReadFile(filepath.Join(tempDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("Grok login did not create auth.json: %w", err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return fmt.Errorf("Grok login created an empty auth.json")
	}
	if name == "" {
		defaultName := uniqueGrokAccountName(sanitizeGrokAccountName(grokAuthEmail(content)))
		if defaultName == "" {
			defaultName = uniqueGrokAccountName("account-" + time.Now().Format("20060102-150405"))
		}
		name, err = promptGrokAccountName(defaultName)
		if err != nil {
			return err
		}
	}
	acc, err := saveGrokAccount(name, content)
	if err != nil {
		return err
	}
	if err := switchGrokAccount(acc); err != nil {
		return err
	}
	fmt.Printf("current Grok account: %s\n", grokAccountLabel(acc))
	return nil
}

func promptGrokAccountName(defaultName string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return defaultName, nil
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("Grok account name [%s]: ", defaultName)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		name := strings.TrimSpace(line)
		if name == "" {
			name = defaultName
		}
		if err := validateGrokAccountName(name); err != nil {
			fmt.Println(err)
			continue
		}
		if _, err := os.Stat(grokAccountPath(name)); err == nil {
			fmt.Printf("Grok account %q already exists.\n", name)
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return name, nil
	}
}

func saveGrokAccount(name string, content []byte) (grokAccount, error) {
	if err := validateGrokAccountName(name); err != nil {
		return grokAccount{}, err
	}
	dir := grokHomeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return grokAccount{}, err
	}
	target := grokAccountPath(name)
	if err := withGrokAccountsLock(func() error {
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("Grok account %q already exists", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return writeFileAtomic(target, content, 0600)
	}); err != nil {
		return grokAccount{}, err
	}
	updated := "-"
	if st, err := os.Stat(target); err == nil {
		updated = st.ModTime().Format("01-02 15:04")
	}
	return grokAccount{
		Name:    name,
		Path:    target,
		Email:   grokAuthEmail(content),
		Updated: updated,
	}, nil
}

func grokAccountPath(name string) string {
	return filepath.Join(grokHomeDir(), "auth."+name+".json")
}

func validateGrokAccountName(name string) error {
	if name == "" {
		return fmt.Errorf("Grok account name is required")
	}
	if name == "json" {
		return fmt.Errorf("Grok account name %q is reserved", name)
	}
	if strings.HasPrefix(name, "backup-") {
		return fmt.Errorf("Grok account name prefix %q is reserved", "backup-")
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("Grok account name %q must use letters, numbers, dot, dash, underscore, plus, at, or colon", name)
	}
	return nil
}

func sanitizeGrokAccountName(value string) string {
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
	if err := validateGrokAccountName(name); err != nil {
		return ""
	}
	return name
}

func uniqueGrokAccountName(base string) string {
	base = sanitizeGrokAccountName(base)
	if base == "" {
		return ""
	}
	if _, err := os.Stat(grokAccountPath(base)); errors.Is(err, os.ErrNotExist) {
		return base
	}
	for i := 2; i < 1000; i++ {
		name := fmt.Sprintf("%s-%d", base, i)
		if _, err := os.Stat(grokAccountPath(name)); errors.Is(err, os.ErrNotExist) {
			return name
		}
	}
	return base + "-" + time.Now().Format("20060102-150405")
}

func listGrokAccounts() ([]grokAccount, error) {
	if err := importCurrentGrokAuthIfNeeded(); err != nil {
		return nil, err
	}
	dir := grokHomeDir()
	currentPath := filepath.Join(dir, "auth.json")
	current, _ := os.ReadFile(currentPath)
	paths, err := grokAccountPaths(dir)
	if err != nil {
		return nil, err
	}
	activeSlot := resolveActiveGrokAccountPath(dir, current, paths)
	accounts := make([]grokAccount, 0, len(paths))
	for _, path := range paths {
		base := filepath.Base(path)
		name := strings.TrimSuffix(strings.TrimPrefix(base, "auth."), ".json")
		if name == "" || name == "json" {
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
		accounts = append(accounts, grokAccount{
			Name:    name,
			Path:    path,
			Email:   grokAuthEmail(content),
			Current: activeSlot != "" && samePath(activeSlot, path),
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

func importCurrentGrokAuthIfNeeded() error {
	dir := grokHomeDir()
	paths, err := grokAccountPaths(dir)
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		return nil
	}
	current, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if errors.Is(err, os.ErrNotExist) || len(bytes.TrimSpace(current)) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	name := uniqueGrokAccountName(sanitizeGrokAccountName(grokAuthEmail(current)))
	if name == "" {
		name = uniqueGrokAccountName("account")
	}
	if name == "" {
		return nil
	}
	acc, err := saveGrokAccount(name, current)
	if err != nil {
		return err
	}
	return saveGrokAccountState(acc.Name)
}

func printGrokAccounts(accounts []grokAccount) error {
	if len(accounts) == 0 {
		fmt.Printf("No Grok account files found in %s\n", grokHomeDir())
		return nil
	}
	for i, acc := range accounts {
		mark := " "
		if acc.Current {
			mark = "*"
		}
		fmt.Printf("%s %d  %s  %s\n", mark, i+1, grokAccountLabel(acc), acc.Updated)
	}
	return nil
}

func grokAccountPicker(accounts []grokAccount, keys *termkey.Reader) (grokAccountAction, error) {
	selected := 0
	for i, acc := range accounts {
		if acc.Current {
			selected = i + 1
			break
		}
	}
	for {
		drawGrokAccountPicker(accounts, selected)
		key, err := keys.Read()
		if err != nil {
			return grokAccountAction{}, err
		}
		rowCount := len(accounts) + 1
		switch {
		case key == "\x1b[A":
			selected = (selected - 1 + rowCount) % rowCount
		case key == "\x1b[B" || key == "j":
			selected = (selected + 1) % rowCount
		case key == "\r" || key == "\n":
			if selected == 0 {
				return grokAccountAction{Action: "new"}, nil
			}
			return grokAccountAction{Action: "change", Account: &accounts[selected-1]}, nil
		case key == "n":
			return grokAccountAction{Action: "new"}, nil
		case key == "l":
			if selected > 0 {
				return grokAccountAction{Action: "login", Account: &accounts[selected-1]}, nil
			}
		case key == "s":
			if selected > 0 {
				return grokAccountAction{Action: "status", Account: &accounts[selected-1]}, nil
			}
		case key == "d" || key == "k" || key == "x":
			if selected == 0 {
				continue
			}
			accountIndex := selected - 1
			if confirm(fmt.Sprintf("Delete %s from Grok accounts? y/N", grokAccountLabel(accounts[accountIndex])), keys) {
				if _, err := deleteGrokAccount(accounts[accountIndex]); err != nil {
					return grokAccountAction{}, err
				}
				var reloadErr error
				accounts, reloadErr = listGrokAccounts()
				if reloadErr != nil {
					return grokAccountAction{}, reloadErr
				}
				if selected > len(accounts) {
					selected = len(accounts)
				}
			}
		case key == "q" || key == "\x1b":
			return grokAccountAction{}, nil
		}
	}
}

func drawGrokAccountPicker(accounts []grokAccount, selected int) {
	width, height, _ := term.GetSize(int(os.Stdout.Fd()))
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 24
	}
	fmt.Print("\033[H\033[2J")
	tuiLine(bold(clip("agemux", width-1)) + clip(" - Grok accounts", max(0, width-1-len("agemux"))))
	tuiLine(dim(clip("Up/Down move  Enter select  n new  l login  s status  d delete  q/Esc back", width-1)))
	tuiLine(strings.Repeat("-", min(width-1, 1000)))
	visible := max(1, (height-5)/3)
	offset := 0
	if selected >= visible {
		offset = selected - visible + 1
	}
	rowCount := len(accounts) + 1
	for idx := offset; idx < min(rowCount, offset+visible); idx++ {
		var lines []string
		if idx == 0 {
			lines = []string{"+ Add Grok account", "    Sign in with grok login and save it as a selectable auth file"}
		} else {
			acc := accounts[idx-1]
			mark := ""
			if acc.Current {
				mark = "* "
			}
			lines = []string{
				fmt.Sprintf("%s%d  %s", mark, idx, grokAccountLabel(acc)),
				"    " + acc.Updated + "  " + filepath.Base(acc.Path),
			}
		}
		active := idx == selected
		for i, line := range lines {
			prefix := "  "
			if active && i == 0 {
				prefix = "> "
			}
			text := padDisplay(prefix+line, width-1)
			if active {
				text = reverse(text)
			} else if i > 0 {
				text = dim(text)
			}
			tuiLine(text)
		}
	}
	fmt.Printf("\033[%d;1H%s", height, dim(clip(fmt.Sprintf("%d Grok account(s)", len(accounts)), width-1)))
}

func switchGrokAccount(acc grokAccount) error {
	dir := grokHomeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return withGrokAccountsLock(func() error {
		if _, err := syncActiveGrokAccount(dir); err != nil {
			return err
		}
		content, err := os.ReadFile(acc.Path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, "auth.json")
		if err := backupUntrackedActiveGrokAuth(dir, target, content); err != nil {
			return err
		}
		if err := writeActiveGrokAuth(content); err != nil {
			return err
		}
		return saveGrokAccountState(acc.Name)
	})
}

func withGrokAccountsLock(fn func() error) error {
	dir := grokHomeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(dir, ".agemux-accounts.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	return fn()
}

func grokAccountStatePath() string {
	return filepath.Join(grokHomeDir(), ".agemux-current-account.json")
}

func loadGrokAccountState() grokAccountState {
	var state grokAccountState
	content, err := os.ReadFile(grokAccountStatePath())
	if err == nil {
		_ = json.Unmarshal(content, &state)
	}
	return state
}

func saveGrokAccountState(name string) error {
	content, err := json.Marshal(grokAccountState{Name: name})
	if err != nil {
		return err
	}
	return writeFileAtomic(grokAccountStatePath(), append(content, '\n'), 0600)
}

func grokAccountPaths(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "auth.*.json"))
	if err != nil {
		return nil, err
	}
	filtered := paths[:0]
	for _, path := range paths {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "auth."), ".json")
		if name == "" || name == "json" || strings.HasPrefix(name, "backup-") {
			continue
		}
		filtered = append(filtered, path)
	}
	return filtered, nil
}

func resolveActiveGrokAccountPath(dir string, current []byte, paths []string) string {
	if len(bytes.TrimSpace(current)) == 0 {
		return ""
	}
	state := loadGrokAccountState()
	if state.Name != "" && validateGrokAccountName(state.Name) == nil {
		path := filepath.Join(dir, "auth."+state.Name+".json")
		if content, err := os.ReadFile(path); err == nil && sameGrokAuthIdentity(current, content) {
			return path
		}
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err == nil && bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(content)) {
			return path
		}
	}
	identity := grokAuthIdentity(current)
	if identity == "" {
		return ""
	}
	match := ""
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil || grokAuthIdentity(content) != identity {
			continue
		}
		if match != "" {
			return ""
		}
		match = path
	}
	return match
}

func syncActiveGrokAccount(dir string) (string, error) {
	current, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	paths, err := grokAccountPaths(dir)
	if err != nil {
		return "", err
	}
	path := resolveActiveGrokAccountPath(dir, current, paths)
	if path == "" {
		return "", nil
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(stored)) {
		if err := writeFileAtomic(path, current, 0600); err != nil {
			return "", err
		}
	}
	return path, nil
}

func grokAuthEmail(content []byte) string {
	var data map[string]any
	if err := json.Unmarshal(content, &data); err != nil {
		return ""
	}
	return findEmail(data)
}

func grokAuthIdentity(content []byte) string {
	email := strings.ToLower(strings.TrimSpace(grokAuthEmail(content)))
	if email == "" {
		return ""
	}
	return "email:" + email
}

func sameGrokAuthIdentity(left, right []byte) bool {
	if bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right)) {
		return true
	}
	leftID := grokAuthIdentity(left)
	return leftID != "" && leftID == grokAuthIdentity(right)
}

func writeActiveGrokAuth(content []byte) error {
	dir := grokHomeDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "auth.json"), content, 0600)
}

func backupUntrackedActiveGrokAuth(dir, target string, replacement []byte) error {
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
