//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Humelo/agemux/internal/terminalstate"
	"github.com/Humelo/agemux/internal/termkey"
	"golang.org/x/term"
)

type grokDiskSession struct {
	ID      string
	Title   string
	Updated time.Time
	Path    string
}

func createGrokResume() error {
	root := rootDir()
	picked, err := pickGrokDiskSession(root)
	if err != nil || picked == nil {
		return err
	}
	if picked.ID == "" {
		return create("grok-fresh")
	}
	names, err := liveSessionNames()
	if err != nil {
		return err
	}
	name := nowName()
	for names[name] {
		name = nowName()
	}
	if err := updateMeta(name, map[string]any{
		"resume_id": picked.ID,
		"title":     firstNonEmpty(picked.Title, "Grok"),
	}); err != nil {
		return err
	}
	return execAttach(name, "grok-resume", false)
}

func pickGrokDiskSession(root string) (*grokDiskSession, error) {
	sessions, err := listGrokDiskSessions(root)
	if err != nil {
		return nil, err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) || os.Getenv("TERM") == "dumb" {
		return pickGrokDiskSessionPlain(sessions)
	}
	return pickGrokDiskSessionTUI(sessions)
}

func pickGrokDiskSessionPlain(sessions []grokDiskSession) (*grokDiskSession, error) {
	fmt.Println()
	fmt.Println("Grok sessions")
	fmt.Println("0. New Grok session")
	for i, session := range sessions {
		fmt.Printf("%d. %s  %s\n", i+1, session.Title, session.ID)
	}
	fmt.Print("\nselect> ")
	var choice string
	if _, err := fmt.Scanln(&choice); err != nil {
		return nil, err
	}
	if choice == "" || choice == "q" {
		return nil, nil
	}
	var idx int
	if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 0 || idx > len(sessions) {
		return nil, fmt.Errorf("selection out of range")
	}
	if idx == 0 {
		return &grokDiskSession{}, nil
	}
	session := sessions[idx-1]
	return &session, nil
}

func pickGrokDiskSessionTUI(sessions []grokDiskSession) (*grokDiskSession, error) {
	screen, err := terminalstate.BeginScreen(os.Stdin, os.Stdout)
	if err != nil {
		return nil, err
	}
	defer screen.Close()
	keys := termkey.NewReader(os.Stdin)
	selected := 0
	for {
		drawGrokSessionPicker(sessions, selected)
		key, err := keys.Read()
		if err != nil {
			return nil, err
		}
		rowCount := len(sessions) + 1
		switch {
		case key == "\x1b[A":
			selected = (selected - 1 + rowCount) % rowCount
		case key == "\x1b[B" || key == "j":
			selected = (selected + 1) % rowCount
		case key == "\r" || key == "\n":
			if selected == 0 {
				return &grokDiskSession{}, nil
			}
			session := sessions[selected-1]
			return &session, nil
		case key == "n":
			return &grokDiskSession{}, nil
		case key == "q" || key == "\x1b":
			return nil, nil
		}
	}
}

func drawGrokSessionPicker(sessions []grokDiskSession, selected int) {
	width, height, _ := term.GetSize(int(os.Stdout.Fd()))
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 24
	}
	fmt.Print("\033[H\033[2J")
	tuiLine(bold(clip("agemux", width-1)) + clip(" - Grok resume picker", max(0, width-1-len("agemux"))))
	tuiLine(dim(clip("Up/Down move  Enter resume  n new  q/Esc back", width-1)))
	tuiLine(strings.Repeat("-", min(width-1, 1000)))
	visible := max(1, (height-5)/2)
	offset := 0
	if selected >= visible {
		offset = selected - visible + 1
	}
	rowCount := len(sessions) + 1
	for idx := offset; idx < min(rowCount, offset+visible); idx++ {
		var lines []string
		if idx == 0 {
			lines = []string{"+ New Grok session", "    Start a fresh Grok conversation"}
		} else {
			session := sessions[idx-1]
			when := "-"
			if !session.Updated.IsZero() {
				when = session.Updated.Local().Format("01-02 15:04")
			}
			lines = []string{session.Title, "    " + when + "  " + session.ID}
		}
		active := idx == selected
		for i, line := range lines {
			prefix := "  "
			if active && i == 0 {
				prefix = "> "
			}
			text := padDisplay(clip(prefix+line, width-1), width-1)
			if active {
				text = reverse(text)
			} else if i > 0 {
				text = dim(text)
			}
			tuiLine(text)
		}
	}
	fmt.Printf("\033[%d;1H%s", height, dim(clip(fmt.Sprintf("%d Grok session(s)", len(sessions)), width-1)))
}

func listGrokDiskSessions(root string) ([]grokDiskSession, error) {
	dirs, err := grokSessionGroupDirs(root)
	if err != nil {
		return nil, err
	}
	var sessions []grokDiskSession
	children := grokSubagentChildIDs(dirs)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !validThreadID(entry.Name()) {
				continue
			}
			if children[strings.ToLower(entry.Name())] {
				continue
			}
			session, ok := readGrokDiskSession(filepath.Join(dir, entry.Name()), entry.Name())
			if ok {
				sessions = append(sessions, session)
			}
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].Updated.After(sessions[j].Updated)
	})
	return sessions, nil
}

func grokSubagentChildIDs(groupDirs []string) map[string]bool {
	children := map[string]bool{}
	for _, dir := range groupDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			metaFiles, err := filepath.Glob(filepath.Join(dir, entry.Name(), "subagents", "*", "meta.json"))
			if err != nil {
				continue
			}
			for _, metaFile := range metaFiles {
				content, err := os.ReadFile(metaFile)
				if err != nil {
					continue
				}
				var meta struct {
					ChildSessionID string `json:"child_session_id"`
					SubagentID     string `json:"subagent_id"`
				}
				if json.Unmarshal(content, &meta) != nil {
					continue
				}
				for _, id := range []string{meta.ChildSessionID, meta.SubagentID} {
					if validThreadID(id) {
						children[strings.ToLower(id)] = true
					}
				}
			}
		}
	}
	return children
}

func grokSessionGroupDirs(root string) ([]string, error) {
	abs, err := filepath.Abs(expandPath(root))
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	base := filepath.Join(grokHomeDir(), "sessions")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	encoded := url.QueryEscape(abs)
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(base, entry.Name())
		if entry.Name() == encoded {
			dirs = append(dirs, path)
			continue
		}
		if decoded, err := url.QueryUnescape(entry.Name()); err == nil && filepath.Clean(decoded) == abs {
			dirs = append(dirs, path)
			continue
		}
		cwd, err := os.ReadFile(filepath.Join(path, ".cwd"))
		if err == nil && filepath.Clean(strings.TrimSpace(string(cwd))) == abs {
			dirs = append(dirs, path)
		}
	}
	return dirs, nil
}

func readGrokDiskSession(dir, id string) (grokDiskSession, bool) {
	content, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return grokDiskSession{}, false
	}
	var summary struct {
		GeneratedTitle  string `json:"generated_title"`
		SessionSummary  string `json:"session_summary"`
		LastTurnSummary string `json:"last_turn_summary"`
		SessionKind     string `json:"session_kind"`
		UpdatedAt       string `json:"updated_at"`
		LastActiveAt    string `json:"last_active_at"`
		CreatedAt       string `json:"created_at"`
		Info            struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	if json.Unmarshal(content, &summary) != nil {
		return grokDiskSession{}, false
	}
	if strings.EqualFold(summary.SessionKind, "subagent") {
		return grokDiskSession{}, false
	}
	if summary.Info.ID != "" && validThreadID(summary.Info.ID) {
		id = summary.Info.ID
	}
	title := firstNonEmpty(summary.GeneratedTitle, summary.SessionSummary, summary.LastTurnSummary, id)
	updated := parseGrokTime(firstNonEmpty(summary.UpdatedAt, summary.LastActiveAt, summary.CreatedAt))
	if updated.IsZero() {
		if st, err := os.Stat(filepath.Join(dir, "summary.json")); err == nil {
			updated = st.ModTime()
		}
	}
	return grokDiskSession{ID: id, Title: title, Updated: updated, Path: dir}, true
}

func parseGrokTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
