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
	if strings.Contains(codexKeyboardSetup, "\x1b[?1004h") {
		t.Fatal("attach-time setup must not enable focus tracking before Codex is ready")
	}
	for _, sequence := range []string{"\x1b[?2004h", "\x1b[>7u"} {
		if !strings.Contains(codexKeyboardSetup, sequence) {
			t.Fatalf("attach-time setup is missing %q", sequence)
		}
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

func TestControlChannelSendsAndCaptures(t *testing.T) {
	t.Setenv("AGEMUX_CONTROL_DIR", t.TempDir())
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
