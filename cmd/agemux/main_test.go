//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Humelo/agemux/internal/terminalstate"
	"github.com/Humelo/agemux/internal/termkey"
)

func TestResolveBinaryUsesPathWithoutPreferredPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "shpool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TEST_SHPOOL_BIN", "")

	if got := resolveBinary("TEST_SHPOOL_BIN", "", "shpool"); got != bin {
		t.Fatalf("resolved %q, want %q", got, bin)
	}
}

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

func TestCodexKeyboardSetupDoesNotEnableFocusTracking(t *testing.T) {
	if strings.Contains(terminalstate.CodexKeyboardSetup, "\x1b[?1004h") {
		t.Fatal("attach-time setup must not enable focus tracking before Codex is ready")
	}
	for _, sequence := range []string{"\x1b[?2004h", "\x1b[>7u"} {
		if !strings.Contains(terminalstate.CodexKeyboardSetup, sequence) {
			t.Fatalf("attach-time setup is missing %q", sequence)
		}
	}
}

func TestConfirmConsumesKeyBufferedByPickerReader(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte("ry")); err != nil {
		t.Fatal(err)
	}
	keys := termkey.NewReader(reader)
	if key, err := keys.Read(); err != nil || key != "r" {
		t.Fatalf("action key = %q, err = %v", key, err)
	}
	if !confirm("Restart? y/N", keys) {
		t.Fatal("buffered confirmation key was not consumed")
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
		"AGEMUX_GROK_BIN",
		"AGEMUX_GROK_DANGEROUS",
		"AGEMUX_PREFIX",
		"AGEMUX_SHPOOL_BIN",
		"CODEX_HOME",
		"GROK_HOME",
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
	t.Setenv("AGEMUX_GROK_DANGEROUS", "")

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

	grokArgs, err := agentArgs("grok-resume", "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if !containsArg(grokArgs, "--always-approve") {
		t.Fatalf("Grok dangerous flag missing by default: %#v", grokArgs)
	}
	if !containsArg(grokArgs, "--cwd") {
		t.Fatalf("Grok cwd flag missing: %#v", grokArgs)
	}
	if containsArg(grokArgs, "--resume") {
		t.Fatalf("Grok welcome picker should not pass --resume without a session ID: %#v", grokArgs)
	}
}

func TestAgentArgsCanDisableDangerousPermissions(t *testing.T) {
	t.Setenv("AGEMUX_CODEX_DANGEROUS", "0")
	t.Setenv("AGEMUX_CLAUDE_DANGEROUS", "false")
	t.Setenv("AGEMUX_GROK_DANGEROUS", "0")

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

	grokArgs, err := agentArgs("grok-resume", "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if containsArg(grokArgs, "--always-approve") {
		t.Fatalf("Grok dangerous flag should be disabled: %#v", grokArgs)
	}
}

func TestAgentArgsUseNamedResumeOptions(t *testing.T) {
	args, err := agentArgsWithMeta("codex-resume", "/tmp/project", map[string]any{
		"resume_id":        "019f-test",
		"model":            "gpt-5.6-sol",
		"reasoning_effort": "xhigh",
		"service_tier":     "default",
		"codex_config":     []any{"notice.hide_rate_limit_model_nudge=true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-m gpt-5.6-sol", "model_reasoning_effort=xhigh", "service_tier=default", "notice.hide_rate_limit_model_nudge=true", "resume 019f-test"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("named Codex args missing %q: %#v", want, args)
		}
	}
	if args[len(args)-2] != "resume" || args[len(args)-1] != "019f-test" {
		t.Fatalf("resume UUID must follow the resume subcommand: %#v", args)
	}
}

func TestGrokAgentArgsUseNamedResumeOptions(t *testing.T) {
	resumeID := "01a0003e" + "-a545-7691-a728-" + "8a6d95595a09"
	args, err := agentArgsWithMeta("grok-resume", "/tmp/project", map[string]any{
		"resume_id":        resumeID,
		"model":            "grok-4.6",
		"reasoning_effort": "xhigh",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--cwd /tmp/project", "--model grok-4.6", "--reasoning-effort xhigh", "--resume " + resumeID} {
		if !strings.Contains(joined, want) {
			t.Fatalf("named Grok args missing %q: %#v", want, args)
		}
	}
}

func TestGrokFreshArgsUseSessionID(t *testing.T) {
	resumeID := "01a0003e" + "-a545-7691-a728-" + "8a6d95595a09"
	args, err := agentArgsWithMeta("grok-fresh", "/tmp/project", map[string]any{"resume_id": resumeID})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--session-id "+resumeID) {
		t.Fatalf("grok-fresh missing --session-id: %#v", args)
	}
	if containsArg(args, "--resume") {
		t.Fatalf("grok-fresh should not pass --resume: %#v", args)
	}
}

func TestGrokFreshArgsRequireSessionID(t *testing.T) {
	_, err := agentArgs("grok-fresh", "/tmp/project")
	if err == nil || !strings.Contains(err.Error(), "requires a session UUID") {
		t.Fatalf("expected grok-fresh UUID error, got %v", err)
	}
}

func TestStartCommandRejectsCodexOnlyFlagsForGrok(t *testing.T) {
	err := startCommand([]string{"grok", "nightly", "--service-tier", "default"})
	if err == nil || !strings.Contains(err.Error(), "--service-tier") {
		t.Fatalf("expected Codex-only flag error, got %v", err)
	}
}

func TestStartNamedGrokCreatesBackgroundSession(t *testing.T) {
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	argsFile := filepath.Join(dir, "args")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[]}'; exit 0; fi\n"+
			"printf '%s\\n' \"$*\" > "+shellQuote(argsFile)+"\n",
	)
	withShpoolBin(t, fake)
	withoutControlReadyWait(t)

	root := filepath.Join(dir, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	resumeID := "01a0003e" + "-a545-7691-a728-" + "8a6d95595a09"
	if err := startNamedSession("grok", "nightly-grok", root, resumeID, "grok-4.6", "xhigh", "", nil, "Grok review", true); err != nil {
		t.Fatal(err)
	}
	row := sessionMeta("nightly-grok")
	if row["provider"] != "grok" || row["kind"] != "grok-resume" || row["resume_id"] != resumeID || row["model"] != "grok-4.6" || row["title"] != "Grok review" {
		t.Fatalf("unexpected Grok session metadata: %#v", row)
	}
}

func TestStartNamedGrokFreshAssignsSessionID(t *testing.T) {
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[]}'; exit 0; fi\n",
	)
	withShpoolBin(t, fake)
	withoutControlReadyWait(t)

	root := filepath.Join(dir, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := startNamedSession("grok", "fresh-grok", root, "", "", "", "", nil, "Fresh Grok", true); err != nil {
		t.Fatal(err)
	}
	row := sessionMeta("fresh-grok")
	if row["kind"] != "grok-fresh" {
		t.Fatalf("fresh start changed kind: %#v", row)
	}
	if !validThreadID(stringValue(row["resume_id"])) {
		t.Fatalf("fresh Grok start did not assign a session UUID: %#v", row)
	}
}

func TestDiscoverGrokSessionIDFromActiveSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROK_HOME", dir)
	resumeID := "01a0003e" + "-a545-7691-a728-" + "8a6d95595a09"
	payload := `[{"session_id":"` + resumeID + `","pid":4242,"cwd":"/tmp/project"}]`
	if err := os.WriteFile(filepath.Join(dir, "active_sessions.json"), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	if got := grokSessionIDForPID(4242); got != resumeID {
		t.Fatalf("grokSessionIDForPID = %q, want %q", got, resumeID)
	}
	if got := grokSessionIDForPID(99); got != "" {
		t.Fatalf("unexpected session for other pid: %q", got)
	}
}

func TestControlChannelSendsAndCaptures(t *testing.T) {
	controlDir, err := os.MkdirTemp("/tmp", "agemux-control-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(controlDir) })
	t.Setenv("AGEMUX_CONTROL_DIR", controlDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var input bytes.Buffer
	writer := &lockedWriter{w: &input}
	output := &outputBuffer{limit: controlOutputLimit}
	output.Append([]byte("first\nsecond\nthird"))
	stop, err := startControlServer(ctx, "agemux-control-test", writer, output)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if _, err := controlCall("agemux-control-test", controlRequest{Op: "send", Text: "line one\nline two", Submit: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := input.String(), "\x1b[200~line one\nline two\x1b[201~\r"; got != want {
		t.Fatalf("control input = %q, want %q", got, want)
	}
	response, err := controlCall("agemux-control-test", controlRequest{Op: "capture", Lines: 2})
	if err != nil {
		t.Fatal(err)
	}
	if response.Output != "second\nthird" {
		t.Fatalf("capture output = %q", response.Output)
	}
	if info, err := os.Stat(controlSocketPath("agemux-control-test")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("control socket mode = %o", info.Mode().Perm())
	}
}

func TestOutputBufferTailPreservesNewlineTerminatedLastLine(t *testing.T) {
	output := &outputBuffer{limit: controlOutputLimit}
	output.Append([]byte("first\nsecond\nthird\n"))

	if got, want := output.Tail(1), "third\n"; got != want {
		t.Fatalf("tail output = %q, want %q", got, want)
	}
	if got, want := output.Tail(2), "second\nthird\n"; got != want {
		t.Fatalf("two-line tail output = %q, want %q", got, want)
	}
}

func TestReadControlInputRejectsOversizedContent(t *testing.T) {
	_, err := readControlInput(strings.NewReader(strings.Repeat("x", controlRequestLimit+1)))
	if err == nil || !strings.Contains(err.Error(), "input exceeds") {
		t.Fatalf("expected bounded input error, got %v", err)
	}
}

func TestPTYWriterReturnsWriteFailureBeforeAcknowledging(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	writerFD := int(writer.Fd())
	if err := syscall.SetNonblock(writerFD, true); err != nil {
		t.Fatal(err)
	}

	chunk := bytes.Repeat([]byte("x"), 4096)
	for {
		if _, err := syscall.Write(writerFD, chunk); errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := newPTYWriter(ctx, writerFD)
	if err := input.EnqueueTimeout([]byte("blocked"), 20*time.Millisecond); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected the blocked PTY write to fail before acknowledgement, got %v", err)
	}
	select {
	case <-input.done:
	case <-time.After(time.Second):
		t.Fatal("PTY writer remained live after a timed-out write")
	}
}

func TestUpsertEnvReplacesTerminal(t *testing.T) {
	env := upsertEnv([]string{"PATH=/bin", "TERM=dumb", "HOME=/tmp/home"}, "TERM", "xterm-256color")
	if got := strings.Join(env, "\n"); strings.Count(got, "TERM=") != 1 || !strings.Contains(got, "TERM=xterm-256color") {
		t.Fatalf("terminal environment was not replaced: %#v", env)
	}
}

func TestStartNamedCodexCreatesBackgroundSession(t *testing.T) {
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	argsFile := filepath.Join(dir, "args")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[]}'; exit 0; fi\n"+
			"printf '%s\\n' \"$*\" > "+shellQuote(argsFile)+"\n",
	)
	withShpoolBin(t, fake)
	withoutControlReadyWait(t)

	root := filepath.Join(dir, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := startNamedCodex("scheduled-story", root, "019f-story", "gpt-5.6-sol", "xhigh", "default", []string{"notice.hide_rate_limit_model_nudge=true"}, "Story translation", true); err != nil {
		t.Fatal(err)
	}
	called, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"attach --background", "--dir " + root, "--cmd", "-- scheduled-story"} {
		if !strings.Contains(string(called), want) {
			t.Fatalf("shpool args missing %q: %q", want, string(called))
		}
	}
	row := sessionMeta("scheduled-story")
	if row["resume_id"] != "019f-story" || row["model"] != "gpt-5.6-sol" || row["title"] != "Story translation" {
		t.Fatalf("unexpected named-session metadata: %#v", row)
	}
}

func TestRegisteredNamedSessionIsOwnedWithoutPrefix(t *testing.T) {
	dir := t.TempDir()
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"scheduled-story\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)
	withMetadataDir(t, dir)
	if err := registerSession("scheduled-story", "codex-resume", "/tmp/project"); err != nil {
		t.Fatal(err)
	}

	sessions, err := agemuxSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || stringValue(sessions[0]["name"]) != "scheduled-story" {
		t.Fatalf("registered named session was not owned: %#v", sessions)
	}
}

func TestStartingReservationSurvivesListBeforeShpoolAppears(t *testing.T) {
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	fake := fakeShpoolScript(t, "if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[]}'; exit 0; fi\n")
	withShpoolBin(t, fake)

	if err := reserveNamedCodexStart("scheduled-starting", "codex-resume", dir, "resume-id", "", "", "", nil, "Starting", "token-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := agemuxSessions(); err != nil {
		t.Fatal(err)
	}
	if row := sessionMeta("scheduled-starting"); !startReservationActive(row) {
		t.Fatalf("starting metadata was removed before shpool appeared: %#v", row)
	}
	if err := reserveNamedCodexStart("scheduled-starting", "codex-resume", dir, "resume-id", "", "", "", nil, "Starting", "token-two"); err == nil || !strings.Contains(err.Error(), "already starting") {
		t.Fatalf("duplicate start reservation was not rejected: %v", err)
	}
}

func TestStartNamedCodexRejectsThreadReservedByAnotherName(t *testing.T) {
	threadID := "019f0000" + "-0000-7000-8000-" + "000000000100"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	fake := fakeShpoolScript(t, "if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[]}'; exit 0; fi\n")
	withShpoolBin(t, fake)
	if err := reserveNamedCodexStart("first-name", "codex-resume", dir, threadID, "", "", "", nil, "First", "token-one"); err != nil {
		t.Fatal(err)
	}

	err := startNamedCodex("second-name", dir, threadID, "", "", "", nil, "Second", true)
	if err == nil || !strings.Contains(err.Error(), "first-name") || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("unexpected duplicate reservation result: %v", err)
	}
}

func TestOldStartCleanupDoesNotDeleteNewReservation(t *testing.T) {
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	fake := fakeShpoolScript(t, "if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[]}'; exit 0; fi\n")
	withShpoolBin(t, fake)

	if err := reserveNamedCodexStart("scheduled-retry", "codex-resume", dir, "resume-id", "", "", "", nil, "Retry", "old-token"); err != nil {
		t.Fatal(err)
	}
	if err := updateMeta("scheduled-retry", map[string]any{"starting_at": time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := reserveNamedCodexStart("scheduled-retry", "codex-resume", dir, "resume-id", "", "", "", nil, "Retry", "new-token"); err != nil {
		t.Fatal(err)
	}
	if err := cleanupReservedStart("scheduled-retry", "old-token"); err != nil {
		t.Fatal(err)
	}
	if row := sessionMeta("scheduled-retry"); row["start_token"] != "new-token" {
		t.Fatalf("old cleanup changed the new reservation: %#v", row)
	}
}

func TestStartNamedCodexFailsWhenControlChannelNeverStarts(t *testing.T) {
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[]}'; exit 0; fi\n"+
			"printf '%s\\n' \"$*\" >> "+shellQuote(calls)+"\n",
	)
	withShpoolBin(t, fake)
	t.Setenv("AGEMUX_CONTROL_DIR", filepath.Join(dir, "control"))

	root := filepath.Join(dir, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	err := startNamedCodex("scheduled-failure", root, "resume-id", "", "", "", nil, "Failure", true)
	if err == nil || !strings.Contains(err.Error(), "exited before its control channel became ready") {
		t.Fatalf("unexpected readiness result: %v", err)
	}
	content, readErr := os.ReadFile(calls)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(content), "attach --background") || !strings.Contains(string(content), "kill -- scheduled-failure") {
		t.Fatalf("failed session was not cleaned up: %q", content)
	}
	if row := sessionMeta("scheduled-failure"); len(row) != 0 {
		t.Fatalf("failed session metadata remains: %#v", row)
	}
}

func TestShpoolSessionsTimesOut(t *testing.T) {
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then exec sleep 2; fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)
	t.Setenv("AGEMUX_SHPOOL_LIST_TIMEOUT", "100ms")

	_, err := shpoolSessions()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestLiveSessionStatesSupportsShortProbeTimeout(t *testing.T) {
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then exec sleep 2; fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)

	started := time.Now()
	_, err := liveSessionStatesWithTimeout(25 * time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("short state probe took %s", elapsed)
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

func TestExecAttachReportsLiveSessionTransportFailure(t *testing.T) {
	withoutAttachRetryDelay(t)
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"if [[ \"$1\" == \"attach\" ]]; then exit 1; fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)

	err := execAttach("agemux-test", "", false)
	if err == nil {
		t.Fatal("expected attach failure")
	}
	if !strings.Contains(err.Error(), "still live (disconnected)") ||
		!strings.Contains(err.Error(), "transport was interrupted or wedged") {
		t.Fatalf("unexpected attach error: %v", err)
	}
}

func TestExecAttachReconnectsAfterTransportFailure(t *testing.T) {
	withoutAttachRetryDelay(t)
	dir := t.TempDir()
	attemptsFile := filepath.Join(dir, "attempts")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"if [[ \"$1\" == \"attach\" ]]; then\n"+
			"  attempts=0\n"+
			"  if [[ -f "+shellQuote(attemptsFile)+" ]]; then attempts=$(cat "+shellQuote(attemptsFile)+"); fi\n"+
			"  attempts=$((attempts + 1))\n"+
			"  printf '%s' \"$attempts\" > "+shellQuote(attemptsFile)+"\n"+
			"  if [[ $attempts -eq 1 ]]; then exit 1; fi\n"+
			"  exit 0\n"+
			"fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)

	if err := execAttach("agemux-test", "", false); err != nil {
		t.Fatal(err)
	}
	attempts, err := os.ReadFile(attemptsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(attempts) != "2" {
		t.Fatalf("attach attempts = %q, want 2", attempts)
	}
}

func TestExecAttachStopsAfterThreeReconnectsAndResetsKeyboardEachTime(t *testing.T) {
	withoutAttachRetryDelay(t)
	dir := t.TempDir()
	attemptsFile := filepath.Join(dir, "attempts")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"if [[ \"$1\" == \"attach\" ]]; then\n"+
			"  printf x >> "+shellQuote(attemptsFile)+"\n"+
			"  exit 1\n"+
			"fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)

	output, attachErr := captureStdout(t, func() error {
		return execAttach("agemux-test", "", false)
	})
	if attachErr == nil {
		t.Fatal("expected attach failure")
	}
	attempts, err := os.ReadFile(attemptsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(attempts) != "xxxx" {
		t.Fatalf("attach attempts = %q, want initial attempt plus three reconnects", attempts)
	}
	if got := strings.Count(output, terminalstate.CodexKeyboardSetup); got != 4 {
		t.Fatalf("keyboard setup count = %d, want 4", got)
	}
	if got := strings.Count(output, terminalstate.KeyboardResetSequence+terminalstate.CodexKeyboardSetup); got != 4 {
		t.Fatalf("reset-before-setup count = %d, want 4", got)
	}
}

func TestExecAttachReconnectsAfterTransientListFailures(t *testing.T) {
	withoutAttachRetryDelay(t)
	dir := t.TempDir()
	listCountFile := filepath.Join(dir, "list-count")
	attachCountFile := filepath.Join(dir, "attach-count")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  count=0\n"+
			"  if [[ -f "+shellQuote(listCountFile)+" ]]; then count=$(cat "+shellQuote(listCountFile)+"); fi\n"+
			"  count=$((count + 1))\n"+
			"  printf '%s' \"$count\" > "+shellQuote(listCountFile)+"\n"+
			"  if [[ $count -eq 2 || $count -eq 3 ]]; then exit 1; fi\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"if [[ \"$1\" == \"attach\" ]]; then\n"+
			"  count=0\n"+
			"  if [[ -f "+shellQuote(attachCountFile)+" ]]; then count=$(cat "+shellQuote(attachCountFile)+"); fi\n"+
			"  count=$((count + 1))\n"+
			"  printf '%s' \"$count\" > "+shellQuote(attachCountFile)+"\n"+
			"  if [[ $count -eq 1 ]]; then exit 1; fi\n"+
			"  exit 0\n"+
			"fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)

	if err := execAttach("agemux-test", "", false); err != nil {
		t.Fatal(err)
	}
	listCalls, err := os.ReadFile(listCountFile)
	if err != nil {
		t.Fatal(err)
	}
	attachCalls, err := os.ReadFile(attachCountFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(listCalls) != "4" || string(attachCalls) != "2" {
		t.Fatalf("list calls = %q, attach calls = %q; want 4 and 2", listCalls, attachCalls)
	}
}

func TestShouldReconnectAttachWaitsForDetachedState(t *testing.T) {
	withoutAttachRetryDelay(t)
	dir := t.TempDir()
	listCountFile := filepath.Join(dir, "list-count")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  count=0\n"+
			"  if [[ -f "+shellQuote(listCountFile)+" ]]; then count=$(cat "+shellQuote(listCountFile)+"); fi\n"+
			"  count=$((count + 1))\n"+
			"  printf '%s' \"$count\" > "+shellQuote(listCountFile)+"\n"+
			"  if [[ $count -eq 1 ]]; then status=Attached; else status=Disconnected; fi\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"%s\"}]}' \"$status\"\n"+
			"  exit 0\n"+
			"fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)

	if !shouldReconnectAttach("agemux-test", exitCodeError(1)) {
		t.Fatal("expected transient attached state to become reconnectable")
	}
	count, err := os.ReadFile(listCountFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "2" {
		t.Fatalf("list calls = %q, want 2", count)
	}
}

func TestExecAttachDoesNotReconnectAfterSessionExit(t *testing.T) {
	withoutAttachRetryDelay(t)
	dir := t.TempDir()
	listCountFile := filepath.Join(dir, "list-count")
	attachCountFile := filepath.Join(dir, "attach-count")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  count=0\n"+
			"  if [[ -f "+shellQuote(listCountFile)+" ]]; then count=$(cat "+shellQuote(listCountFile)+"); fi\n"+
			"  count=$((count + 1))\n"+
			"  printf '%s' \"$count\" > "+shellQuote(listCountFile)+"\n"+
			"  if [[ $count -eq 1 ]]; then printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Disconnected\"}]}'; else printf '{\"sessions\":[]}'; fi\n"+
			"  exit 0\n"+
			"fi\n"+
			"if [[ \"$1\" == \"attach\" ]]; then\n"+
			"  printf x >> "+shellQuote(attachCountFile)+"\n"+
			"  exit 1\n"+
			"fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)

	if err := execAttach("agemux-test", "", false); err == nil {
		t.Fatal("expected attach failure")
	}
	attempts, err := os.ReadFile(attachCountFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(attempts) != "x" {
		t.Fatalf("attach attempts = %q, want one", attempts)
	}
}

func TestCodexThreadIDFromRolloutPath(t *testing.T) {
	threadID := "019f87ce" + "-e841-7b40-90da-" + "554c1ba9da6a"
	path := "/tmp/custom-codex-home/sessions/2026/07/22/rollout-2026-07-22T12-11-51-" + threadID + ".jsonl"
	if got := codexThreadIDFromRolloutPath(path); got != threadID {
		t.Fatalf("thread ID = %q, want %q", got, threadID)
	}
	if got := codexThreadIDFromRolloutPath("/tmp/rollout-" + threadID + ".jsonl"); got != "" {
		t.Fatalf("accepted rollout outside Codex sessions directory: %q", got)
	}
}

func TestCodexThreadIDForPIDReadsOpenRolloutFile(t *testing.T) {
	threadID := "019f87ce" + "-e841-7b40-90da-" + "554c1ba9da6a"
	dir := filepath.Join(t.TempDir(), "custom-codex-home", "sessions", "2026", "07", "25")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(dir, "rollout-2026-07-25T12-00-00-"+threadID+".jsonl")
	file, err := os.OpenFile(rollout, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writeRolloutSessionMeta(t, file, threadID, "")

	if got := codexThreadIDForPID(os.Getpid()); got != threadID {
		t.Fatalf("thread ID from /proc/self/fd = %q, want %q", got, threadID)
	}
}

func TestCodexThreadIDForPIDIgnoresSubagentRollouts(t *testing.T) {
	rootID := "019f0000" + "-0000-7000-8000-" + "000000000100"
	subagentID := "019f0000" + "-0000-7000-8000-" + "000000000200"
	dir := filepath.Join(t.TempDir(), "codex", "sessions", "2026", "08", "01")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenFile(filepath.Join(dir, "rollout-root-"+rootID+".jsonl"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	writeRolloutSessionMeta(t, root, rootID, "")
	subagent, err := os.OpenFile(filepath.Join(dir, "rollout-subagent-"+subagentID+".jsonl"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer subagent.Close()
	writeRolloutSessionMeta(t, subagent, subagentID, rootID)

	if got := codexThreadIDForPID(os.Getpid()); got != rootID {
		t.Fatalf("thread ID with subagent rollout = %q, want root %q", got, rootID)
	}
}

func TestTrackedCodexThreadIDCannotOverwriteAnotherAgent(t *testing.T) {
	originalID := "019f0000" + "-0000-7000-8000-" + "000000000100"
	replacementID := "019f0000" + "-0000-7000-8000-" + "000000000200"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	if err := updateMeta("tracked-session", map[string]any{
		"agent_pid": 100,
		"resume_id": originalID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := updateTrackedCodexThreadID("tracked-session", 200, replacementID); err != nil {
		t.Fatal(err)
	}
	if got := stringValue(sessionMeta("tracked-session")["resume_id"]); got != originalID {
		t.Fatalf("stale tracker changed resume ID to %q", got)
	}

	if err := withMetaLock(func(meta metadata) error {
		delete(meta, "tracked-session")
		return saveMetaUnlocked(meta)
	}); err != nil {
		t.Fatal(err)
	}
	if err := updateTrackedCodexThreadID("tracked-session", 100, originalID); err != nil {
		t.Fatal(err)
	}
	if row := sessionMeta("tracked-session"); len(row) != 0 {
		t.Fatalf("stale tracker recreated deleted metadata: %#v", row)
	}
}

func TestTrackedCodexThreadIDUpdatesWhenSameAgentChangesThread(t *testing.T) {
	originalID := "019f0000" + "-0000-7000-8000-" + "000000000100"
	replacementID := "019f0000" + "-0000-7000-8000-" + "000000000200"
	withMetadataDir(t, filepath.Join(t.TempDir(), "data"))
	if err := updateMeta("tracked-session", map[string]any{
		"agent_pid": 100,
		"resume_id": originalID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := updateTrackedCodexThreadID("tracked-session", 100, replacementID); err != nil {
		t.Fatal(err)
	}
	if got := stringValue(sessionMeta("tracked-session")["resume_id"]); got != replacementID {
		t.Fatalf("thread change was not tracked: got %q, want %q", got, replacementID)
	}
}

func TestClaimTrackedCodexThreadIDRejectsLiveOwner(t *testing.T) {
	const (
		name     = "picker-session"
		owner    = "scheduled-session"
		threadID = "019f0000" + "-0000-7000-8000-000000000200"
	)
	withMetadataDir(t, filepath.Join(t.TempDir(), "data"))
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == owner {
			return threadID
		}
		return ""
	})
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"},{\"name\":\""+owner+"\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)
	for _, sessionName := range []string{name, owner} {
		if err := updateMeta(sessionName, map[string]any{
			"agent_pid": 100,
			"kind":      "codex-resume",
		}); err != nil {
			t.Fatal(err)
		}
	}

	owners, err := claimTrackedCodexThreadID(name, 100, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(owners, ",") != owner {
		t.Fatalf("owners = %v, want %s", owners, owner)
	}
	if got := stringValue(sessionMeta(name)["resume_id"]); got != "" {
		t.Fatalf("duplicate picker claimed thread %q", got)
	}
}

func TestRestartConfirmationPromptShowsSessionAndThread(t *testing.T) {
	const threadID = "019f0000" + "-0000-7000-8000-000000000300"
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == "internal-name" {
			return threadID
		}
		return ""
	})
	prompt := restartConfirmationPrompt(menuItem{Type: "session", Name: "internal-name", Label: "Visible title"})
	for _, value := range []string{"Visible title", "internal-name", threadID} {
		if !strings.Contains(prompt, value) {
			t.Fatalf("confirmation %q is missing %q", prompt, value)
		}
	}
}

func TestRestartCodexSessionPreservesThreadAndLaunchSettings(t *testing.T) {
	const name = "restart-preserves-settings"
	threadID := "019f87ce" + "-e841-7b40-90da-" + "554c1ba9da6a"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == name {
			return threadID
		}
		return ""
	})
	withoutControlReadyWait(t)
	killed := filepath.Join(dir, "killed")
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  if [[ -f "+shellQuote(killed)+" ]]; then printf '{\"sessions\":[]}'; else printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"}]}'; fi\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf '%s\\n' \"$*\" >> "+shellQuote(calls)+"\n"+
			"if [[ \"$1\" == \"kill\" ]]; then touch "+shellQuote(killed)+"; fi\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	root := filepath.Join(dir, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := updateMeta(name, map[string]any{
		"provider":         "codex",
		"kind":             "codex-resume",
		"root":             root,
		"title":            "Exact thread",
		"resume_id":        threadID,
		"model":            "gpt-5.6-sol",
		"reasoning_effort": "xhigh",
		"service_tier":     "priority",
		"codex_config":     []string{"agents.max_threads=16", "notice.hide_rate_limit_model_nudge=true"},
	}); err != nil {
		t.Fatal(err)
	}

	output, err := captureStdout(t, func() error { return restartCodexSession(name) })
	if err != nil {
		t.Fatal(err)
	}
	if output != "restarted "+name+" as Codex session "+threadID+"\n" {
		t.Fatalf("restart output = %q", output)
	}
	called, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(called), "kill -- "+name) || !strings.Contains(string(called), "attach --background") {
		t.Fatalf("restart did not kill and relaunch the session: %q", called)
	}
	row := sessionMeta(name)
	if row["resume_id"] != threadID || row["model"] != "gpt-5.6-sol" || row["reasoning_effort"] != "xhigh" || row["service_tier"] != "priority" || row["title"] != "Exact thread" {
		t.Fatalf("restart changed launch metadata: %#v", row)
	}
	if got := stringSliceValue(row["codex_config"]); strings.Join(got, "|") != "agents.max_threads=16|notice.hide_rate_limit_model_nudge=true" {
		t.Fatalf("restart changed Codex config: %#v", got)
	}
}

func TestRestartCodexSessionUsesLiveThreadInsteadOfStaleMetadata(t *testing.T) {
	const name = "restart-stale-thread"
	storedID := "019f0000" + "-0000-7000-8000-" + "000000000100"
	liveID := "019f0000" + "-0000-7000-8000-" + "000000000200"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == name {
			return liveID
		}
		return ""
	})
	withoutControlReadyWait(t)
	killed := filepath.Join(dir, "killed")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  if [[ -f "+shellQuote(killed)+" ]]; then printf '{\"sessions\":[]}'; else printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"}]}'; fi\n"+
			"  exit 0\n"+
			"fi\n"+
			"if [[ \"$1\" == \"kill\" ]]; then touch "+shellQuote(killed)+"; fi\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	if err := updateMeta(name, map[string]any{
		"provider":  "codex",
		"kind":      "codex-resume",
		"root":      dir,
		"resume_id": storedID,
	}); err != nil {
		t.Fatal(err)
	}

	output, err := captureStdout(t, func() error { return restartCodexSession(name) })
	if err != nil {
		t.Fatal(err)
	}
	if output != "restarted "+name+" as Codex session "+liveID+"\n" {
		t.Fatalf("restart output = %q", output)
	}
	if got := stringValue(sessionMeta(name)["resume_id"]); got != liveID {
		t.Fatalf("restart kept stale thread %q, want live thread %q", got, liveID)
	}
}

func TestRestartCodexSessionRejectsThreadOwnedByAnotherLiveSession(t *testing.T) {
	const (
		name  = "restart-duplicate"
		owner = "scheduled-owner"
	)
	threadID := "019f87ce" + "-e841-7b40-90da-" + "554c1ba9da6a"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == name || sessionName == owner {
			return threadID
		}
		return ""
	})
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"},{\"name\":\""+owner+"\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf x > "+shellQuote(calls)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	for _, sessionName := range []string{name, owner} {
		if err := updateMeta(sessionName, map[string]any{
			"provider":  "codex",
			"kind":      "codex-resume",
			"root":      dir,
			"resume_id": threadID,
		}); err != nil {
			t.Fatal(err)
		}
	}

	err := restartCodexSession(name)
	if err == nil || !strings.Contains(err.Error(), owner) || !strings.Contains(err.Error(), "restart refused") {
		t.Fatalf("unexpected restart result: %v", err)
	}
	if _, statErr := os.Stat(calls); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("duplicate thread reached shpool mutation: %v", statErr)
	}
}

func TestRestartCodexSessionRejectsOwnerMissingStoredThreadID(t *testing.T) {
	const (
		name  = "restart-duplicate-with-legacy-owner"
		owner = "agemux-legacy-owner"
	)
	threadID := "019f87ce" + "-e841-7b40-90da-" + "554c1ba9da6a"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == name || sessionName == owner {
			return threadID
		}
		return ""
	})
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"},{\"name\":\""+owner+"\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf x > "+shellQuote(calls)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	if err := updateMeta(name, map[string]any{"provider": "codex", "kind": "codex-resume", "root": dir, "resume_id": threadID}); err != nil {
		t.Fatal(err)
	}
	err := restartCodexSession(name)
	if err == nil || !strings.Contains(err.Error(), owner) || !strings.Contains(err.Error(), "restart refused") {
		t.Fatalf("unexpected restart result: %v", err)
	}
	if _, statErr := os.Stat(calls); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy duplicate reached shpool mutation: %v", statErr)
	}
}

func TestRestartCodexSessionRejectsUnverifiedThreadWithoutKilling(t *testing.T) {
	const name = "restart-without-thread-id"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"}]}'; exit 0; fi\n"+
			"printf x > "+shellQuote(calls)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	if err := updateMeta(name, map[string]any{"provider": "codex", "kind": "codex-resume", "root": dir}); err != nil {
		t.Fatal(err)
	}

	err := restartCodexSession(name)
	if err == nil || !strings.Contains(err.Error(), "could not determine the active Codex session UUID") {
		t.Fatalf("unexpected restart result: %v", err)
	}
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverified session reached shpool kill: %v", err)
	}
}

func TestRestartCodexSessionRejectsClaudeWithoutKilling(t *testing.T) {
	const name = "restart-claude"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"}]}'; exit 0; fi\n"+
			"printf x > "+shellQuote(calls)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	if err := updateMeta(name, map[string]any{"provider": "claude", "kind": "claude-resume", "root": dir}); err != nil {
		t.Fatal(err)
	}

	err := restartCodexSession(name)
	if err == nil || !strings.Contains(err.Error(), "is not a Codex or Grok session") {
		t.Fatalf("unexpected restart result: %v", err)
	}
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Claude session reached shpool kill: %v", err)
	}
}

func TestRestartGrokSessionPreservesLaunchSettings(t *testing.T) {
	const name = "restart-grok-settings"
	threadID := "01a0003e" + "-a545-7691-a728-" + "8a6d95595a09"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == name {
			return threadID
		}
		return ""
	})
	withoutControlReadyWait(t)
	killed := filepath.Join(dir, "killed")
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  if [[ -f "+shellQuote(killed)+" ]]; then printf '{\"sessions\":[]}'; else printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"}]}'; fi\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf '%s\\n' \"$*\" >> "+shellQuote(calls)+"\n"+
			"if [[ \"$1\" == \"kill\" ]]; then touch "+shellQuote(killed)+"; fi\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	root := filepath.Join(dir, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := updateMeta(name, map[string]any{
		"provider":         "grok",
		"kind":             "grok-resume",
		"root":             root,
		"title":            "Grok thread",
		"resume_id":        threadID,
		"model":            "grok-4.6",
		"reasoning_effort": "xhigh",
	}); err != nil {
		t.Fatal(err)
	}

	output, err := captureStdout(t, func() error { return restartCodexSession(name) })
	if err != nil {
		t.Fatal(err)
	}
	if output != "restarted "+name+" as Grok session "+threadID+"\n" {
		t.Fatalf("restart output = %q", output)
	}
	row := sessionMeta(name)
	if row["resume_id"] != threadID || row["model"] != "grok-4.6" || row["reasoning_effort"] != "xhigh" || row["title"] != "Grok thread" {
		t.Fatalf("restart changed Grok launch metadata: %#v", row)
	}
}

func TestRestartConfirmationPromptUsesGrokLabel(t *testing.T) {
	const threadID = "01a0003e" + "-a545-7691-a728-" + "8a6d95595a09"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	if err := updateMeta("grok-session", map[string]any{"provider": "grok", "kind": "grok-resume"}); err != nil {
		t.Fatal(err)
	}
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == "grok-session" {
			return threadID
		}
		return ""
	})
	prompt := restartConfirmationPrompt(menuItem{Type: "session", Name: "grok-session", Label: "Visible grok"})
	for _, value := range []string{"Visible grok", "grok-session", threadID, "Grok session"} {
		if !strings.Contains(prompt, value) {
			t.Fatalf("confirmation %q is missing %q", prompt, value)
		}
	}
}

func TestGrokAccountsListAndSwitchUseGrokHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROK_HOME", dir)

	alpha := fakeGrokAuth("alpha@example.invalid")
	beta := fakeGrokAuth("beta@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "auth.alpha.json"), []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.beta.json"), []byte(beta), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(beta), 0600); err != nil {
		t.Fatal(err)
	}

	accounts, err := listGrokAccounts()
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

	var alphaAccount grokAccount
	for _, acc := range accounts {
		if acc.Name == "alpha" {
			alphaAccount = acc
			break
		}
	}
	if alphaAccount.Name == "" {
		t.Fatal("missing alpha account")
	}
	if err := switchGrokAccount(alphaAccount); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != alpha {
		t.Fatalf("auth.json was not switched: %q", string(current))
	}
}

func TestGrokAccountsImportCurrentAuthFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROK_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(fakeGrokAuth("solo@example.invalid")), 0600); err != nil {
		t.Fatal(err)
	}
	accounts, err := listGrokAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || !accounts[0].Current || accounts[0].Email != "solo@example.invalid" {
		t.Fatalf("imported accounts = %#v", accounts)
	}
}

func fakeGrokAuth(email string) string {
	return `{"https://auth.x.ai::slot":{"email":"` + email + `"}}`
}

func TestDrawActionGridRendersThreeProviderColumns(t *testing.T) {
	items := []menuItem{
		{Type: "codex-resume", Label: "Codex resume picker"},
		{Type: "claude-resume", Label: "Claude resume picker"},
		{Type: "grok-resume", Label: "Grok resume picker"},
		{Type: "codex-fresh", Label: "New Codex"},
		{Type: "claude-fresh", Label: "New Claude"},
		{Type: "grok-fresh", Label: "New Grok"},
		{Type: "codex-accounts", Label: "Codex accounts"},
		{Type: "claude-accounts", Label: "Claude accounts"},
	}
	output, err := captureStdout(t, func() error {
		drawActionGrid(items, 0, 90)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 action rows, got %d: %q", len(lines), output)
	}
	if !strings.Contains(lines[0], "Codex resume picker") || !strings.Contains(lines[0], "Claude resume picker") || !strings.Contains(lines[0], "Grok resume picker") {
		t.Fatalf("first row should be Codex | Claude | Grok resume: %q", lines[0])
	}
	if !strings.Contains(lines[1], "New Codex") || !strings.Contains(lines[1], "New Claude") || !strings.Contains(lines[1], "New Grok") {
		t.Fatalf("second row should be New Codex | New Claude | New Grok: %q", lines[1])
	}
}

func TestMoveSelectionRightUsesThreeColumns(t *testing.T) {
	if got := moveSelectionRight(0, 8); got != 1 {
		t.Fatalf("right from Codex resume = %d, want 1", got)
	}
	if got := moveSelectionRight(1, 8); got != 2 {
		t.Fatalf("right from Claude resume = %d, want 2", got)
	}
	if got := moveSelectionRight(2, 8); got != 2 {
		t.Fatalf("right from Grok resume should stay on last column, got %d", got)
	}
	if got := moveSelectionDown(0, 8, 0); got != 3 {
		t.Fatalf("down from Codex resume = %d, want New Codex", got)
	}
}

func TestRestartCodexSessionValidatesRootBeforeKilling(t *testing.T) {
	const name = "restart-invalid-root"
	threadID := "019f87ce" + "-e841-7b40-90da-" + "554c1ba9da6a"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == name {
			return threadID
		}
		return ""
	})
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"}]}'; exit 0; fi\n"+
			"printf x > "+shellQuote(calls)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	if err := updateMeta(name, map[string]any{
		"provider":  "codex",
		"kind":      "codex-resume",
		"resume_id": threadID,
		"root":      filepath.Join(dir, "missing"),
	}); err != nil {
		t.Fatal(err)
	}

	err := restartCodexSession(name)
	if err == nil || !strings.Contains(err.Error(), "invalid root") {
		t.Fatalf("unexpected restart result: %v", err)
	}
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid-root session reached shpool kill: %v", err)
	}
}

func TestRestartCodexSessionRejectsMissingRecordedRoot(t *testing.T) {
	const name = "restart-missing-root"
	threadID := "019f87ce" + "-e841-7b40-90da-" + "554c1ba9da6a"
	dir := t.TempDir()
	withMetadataDir(t, filepath.Join(dir, "data"))
	withCodexThreadDiscovery(t, func(sessionName string) string {
		if sessionName == name {
			return threadID
		}
		return ""
	})
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then printf '{\"sessions\":[{\"name\":\""+name+"\",\"status\":\"Disconnected\"}]}'; exit 0; fi\n"+
			"printf x > "+shellQuote(calls)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	if err := updateMeta(name, map[string]any{"provider": "codex", "kind": "codex-resume", "resume_id": threadID}); err != nil {
		t.Fatal(err)
	}

	err := restartCodexSession(name)
	if err == nil || !strings.Contains(err.Error(), "missing its recorded root") {
		t.Fatalf("unexpected restart result: %v", err)
	}
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-root session reached shpool kill: %v", err)
	}
}

func TestKillSessionRefusesUnownedShpoolSession(t *testing.T) {
	dir := t.TempDir()
	called := filepath.Join(dir, "called")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"foreign-session\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf '%s\\n' \"$*\" > "+shellQuote(called)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	withMetadataDir(t, dir)

	err := killSession("foreign-session")
	if err == nil || !strings.Contains(err.Error(), "no live agemux session") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(called); !os.IsNotExist(statErr) {
		t.Fatalf("unowned session reached shpool kill: %v", statErr)
	}
}

func TestKillSessionRepairsDisconnectedStaleSession(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	killCount := filepath.Join(dir, "kill-count")
	fake := fakeShpoolScript(t,
		"printf '%s\\n' \"$*\" >> "+shellQuote(calls)+"\n"+
			"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"if [[ \"$1\" == \"kill\" ]]; then\n"+
			"  count=0\n"+
			"  if [[ -f "+shellQuote(killCount)+" ]]; then count=$(cat "+shellQuote(killCount)+"); fi\n"+
			"  count=$((count + 1))\n"+
			"  printf '%s' \"$count\" > "+shellQuote(killCount)+"\n"+
			"  [[ $count -gt 1 ]]\n"+
			"  exit\n"+
			"fi\n"+
			"if [[ \"$1 $2\" == \"attach --background\" ]]; then exit 0; fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)
	withMetadataDir(t, dir)
	if err := registerSession("agemux-test", "codex-resume", "/tmp/project"); err != nil {
		t.Fatal(err)
	}

	if err := killSession("agemux-test"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"list --json",
		"kill -- agemux-test",
		"attach --background -- agemux-test",
		"kill -- agemux-test",
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("missing call %q in %q", want, content)
		}
	}
}

func TestKillSessionDoesNotRepairAttachedSession(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := fakeShpoolScript(t,
		"printf '%s\\n' \"$*\" >> "+shellQuote(calls)+"\n"+
			"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Attached\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"if [[ \"$1\" == \"kill\" ]]; then exit 1; fi\n"+
			"exit 2\n",
	)
	withShpoolBin(t, fake)
	withMetadataDir(t, dir)
	if err := registerSession("agemux-test", "codex-resume", "/tmp/project"); err != nil {
		t.Fatal(err)
	}

	err := killSession("agemux-test")
	if err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("unexpected error: %v", err)
	}
	content, readErr := os.ReadFile(calls)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(content), "attach --background") {
		t.Fatalf("attached session entered stale repair: %q", content)
	}
}

func TestDetachSessionPreservesMetadata(t *testing.T) {
	dir := t.TempDir()
	called := filepath.Join(dir, "called")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Attached\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf '%s' \"$*\" > "+shellQuote(called)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	withMetadataDir(t, dir)
	if err := registerSession("agemux-test", "codex-resume", "/tmp/project"); err != nil {
		t.Fatal(err)
	}

	if err := detachSession("agemux-test"); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(called)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "detach agemux-test" {
		t.Fatalf("detach args = %q", args)
	}
	meta, err := loadMetaUnlocked()
	if err != nil {
		t.Fatal(err)
	}
	row := meta["agemux-test"]
	if row == nil || row["kind"] != "codex-resume" {
		t.Fatalf("session metadata was not preserved: %#v", row)
	}
}

func TestDetachSessionIsNoopWhenDisconnected(t *testing.T) {
	dir := t.TempDir()
	called := filepath.Join(dir, "called")
	fake := fakeShpoolScript(t,
		"if [[ \"$1 $2\" == \"list --json\" ]]; then\n"+
			"  printf '{\"sessions\":[{\"name\":\"agemux-test\",\"status\":\"Disconnected\"}]}'\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf x > "+shellQuote(called)+"\n"+
			"exit 0\n",
	)
	withShpoolBin(t, fake)
	withMetadataDir(t, dir)
	if err := registerSession("agemux-test", "codex-resume", "/tmp/project"); err != nil {
		t.Fatal(err)
	}

	if err := detachSession("agemux-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(called); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disconnected session reached shpool detach: %v", err)
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

func withoutAttachRetryDelay(t *testing.T) {
	t.Helper()
	old := attachRetrySleep
	attachRetrySleep = func(time.Duration) {}
	t.Cleanup(func() {
		attachRetrySleep = old
	})
}

func withoutControlReadyWait(t *testing.T) {
	t.Helper()
	old := waitControlReady
	waitControlReady = func(string) error { return nil }
	t.Cleanup(func() {
		waitControlReady = old
	})
}

func withCodexThreadDiscovery(t *testing.T, discover func(string) string) {
	t.Helper()
	previous := discoverThreadID
	discoverThreadID = discover
	t.Cleanup(func() { discoverThreadID = previous })
}

func writeRolloutSessionMeta(t *testing.T, file *os.File, threadID, parentThreadID string) {
	t.Helper()
	source := any("cli")
	parent := any(nil)
	if parentThreadID != "" {
		source = map[string]any{
			"subagent": map[string]any{
				"thread_spawn": map[string]any{
					"parent_thread_id": parentThreadID,
					"depth":            1,
				},
			},
		}
		parent = parentThreadID
	}
	event := map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"id":               threadID,
			"source":           source,
			"parent_thread_id": parent,
		},
	}
	if err := json.NewEncoder(file).Encode(event); err != nil {
		t.Fatal(err)
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = old }()
	runErr := fn()
	os.Stdout = old
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content), runErr
}

func withMetadataDir(t *testing.T, dir string) {
	t.Helper()
	oldDataDir := dataDir
	oldMetaFile := metaFile
	oldLockFile := lockFile
	dataDir = dir
	metaFile = filepath.Join(dir, "sessions.json")
	lockFile = filepath.Join(dir, "sessions.lock")
	t.Cleanup(func() {
		dataDir = oldDataDir
		metaFile = oldMetaFile
		lockFile = oldLockFile
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

func TestSwitchCodexAccountPersistsRefreshedActiveAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)

	alphaOld := fakeCodexAuthVersion("alpha@example.invalid", "old")
	alphaRefreshed := fakeCodexAuthVersion("alpha@example.invalid", "refreshed")
	beta := fakeCodexAuthVersion("beta@example.invalid", "current")
	alphaPath := filepath.Join(dir, "auth.alpha.json")
	betaPath := filepath.Join(dir, "auth.beta.json")
	if err := os.WriteFile(alphaPath, []byte(alphaOld), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(betaPath, []byte(beta), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(alphaRefreshed), 0600); err != nil {
		t.Fatal(err)
	}

	accounts, err := listCodexAccounts(false)
	if err != nil {
		t.Fatal(err)
	}
	if current := currentCodexAccount(accounts); current == nil || current.Name != "alpha" {
		t.Fatalf("refreshed active account was not recognized: %#v", current)
	}
	if err := switchCodexAccount(codexAccount{Name: "beta", Path: betaPath}); err != nil {
		t.Fatal(err)
	}
	synced, err := os.ReadFile(alphaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(synced) != alphaRefreshed {
		t.Fatalf("refreshed credentials were not synced: %q", string(synced))
	}
	active, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != beta {
		t.Fatalf("active credentials = %q", string(active))
	}
	backups, err := filepath.Glob(filepath.Join(dir, "auth.backup-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("refreshed managed auth was backed up as untracked: %#v", backups)
	}
}

func TestSaveCodexAccountSerializesDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)

	contents := []string{
		fakeCodexAuth("one@example.invalid"),
		fakeCodexAuth("two@example.invalid"),
	}
	errs := make([]error, len(contents))
	var wg sync.WaitGroup
	for i := range contents {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, errs[index] = saveCodexAccount("shared", []byte(contents[index]))
		}(i)
	}
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("unexpected save error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful saves = %d, errors = %#v", successes, errs)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "auth.shared.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != contents[0] && string(stored) != contents[1] {
		t.Fatalf("stored credentials were overwritten or corrupted: %q", string(stored))
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
	return fakeCodexAuthVersion(email, "")
}

func fakeCodexAuthVersion(email, version string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `"}`))
	return `{"version":"` + version + `","tokens":{"id_token":"header.` + payload + `.sig"}}`
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
