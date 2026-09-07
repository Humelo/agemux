//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeNamedResumeArgsAndFreshPickerCompatibility(t *testing.T) {
	const id = "11111111" + "-1111-4111-8111-" + "111111111111"
	for _, tc := range []struct {
		kind string
		id   string
		want string
	}{
		{"claude-resume", id, "--resume " + id},
		{"claude-resume", "", "--resume"},
		{"claude-fresh", "", ""},
	} {
		args, err := agentArgsWithMeta(tc.kind, t.TempDir(), map[string]any{"resume_id": tc.id})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		if tc.want == "" {
			if containsArg(args, "--resume") || containsArg(args, "--session-id") {
				t.Fatalf("fresh Claude must start without a picker: %#v", args)
			}
		} else if !strings.HasSuffix(joined, tc.want) {
			t.Fatalf("want suffix %q, got %#v", tc.want, args)
		}
	}
}

func TestStartClaudeRejectsUnsupportedOptions(t *testing.T) {
	for _, flag := range []string{"--model", "--effort", "--service-tier", "--config", "--unknown"} {
		err := startCommand([]string{"claude", "claude-test", flag, "test=value"})
		if err == nil || !strings.Contains(err.Error(), flag) || strings.Contains(err.Error(), "usage:") {
			t.Fatalf("%s must fail as an unsupported Claude option: %v", flag, err)
		}
	}
	for _, flag := range []string{"--resume", "--root", "--title"} {
		err := startCommand([]string{"claude", "claude-test", flag})
		if err == nil || !strings.Contains(err.Error(), flag+" requires") {
			t.Fatalf("missing %s argument should be rejected: %v", flag, err)
		}
	}
}

func TestStartUsageIncludesClaude(t *testing.T) {
	err := startCommand(nil)
	if err == nil || !strings.Contains(err.Error(), "codex|grok|claude") {
		t.Fatalf("start usage missing Claude: %v", err)
	}
}

func TestStartClaudeCreatesBackgroundSession(t *testing.T) {
	for _, resumeID := range []string{"", "11111111" + "-1111-4111-8111-" + "111111111111"} {
		t.Run("resume="+resumeID, func(t *testing.T) {
			dir := t.TempDir()
			withMetadataDir(t, filepath.Join(dir, "data"))
			argsFile := filepath.Join(dir, "args")
			fake := fakeShpoolScript(t,
				"if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[]}'; exit 0; fi\n"+
					"printf '%s\\n' \"$*\" > "+shellQuote(argsFile)+"\n")
			withShpoolBin(t, fake)
			withoutControlReadyWait(t)
			args := []string{"claude", "claude-test", "--background", "--root", dir, "--title", "Claude test"}
			kind := "claude-fresh"
			if resumeID != "" {
				args = append(args, "--resume", resumeID)
				kind = "claude-resume"
			}
			if err := startCommand(args); err != nil {
				t.Fatal(err)
			}
			called, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"attach --background", "--dir " + dir, kind, "-- claude-test"} {
				if !strings.Contains(string(called), want) {
					t.Fatalf("shpool invocation missing %q: %s", want, called)
				}
			}
			row := sessionMeta("claude-test")
			if row["provider"] != "claude" || row["kind"] != kind || row["root"] != dir || row["title"] != "Claude test" || stringValue(row["resume_id"]) != resumeID || stringValue(row["created_at"]) == "" {
				t.Fatalf("unexpected Claude metadata: %#v", row)
			}
		})
	}
}
