package terminalstate

import (
	"bytes"
	"strings"
	"testing"
)

func TestKeyboardResetEmptiesNestedProtocolStack(t *testing.T) {
	for _, sequence := range []string{
		"\033[<99u",
		"\033[>4;0m",
		"\033[?1004l",
		"\033[?2004l",
	} {
		if !strings.Contains(KeyboardResetSequence, sequence) {
			t.Fatalf("reset sequence is missing %q", sequence)
		}
	}
	if strings.Contains(KeyboardResetSequence, "\033[<u") {
		t.Fatal("reset must not pop only one keyboard-protocol entry")
	}
}

func TestResetWritesCompleteSequence(t *testing.T) {
	var output bytes.Buffer
	Reset(&output)
	if output.String() != KeyboardResetSequence {
		t.Fatalf("reset output = %q", output.String())
	}
}

func TestResetAttachmentLeavesAlternateScreen(t *testing.T) {
	var output bytes.Buffer
	ResetAttachment(&output)
	if got, want := output.String(), leaveScreenSequence+KeyboardResetSequence; got != want {
		t.Fatalf("attachment reset = %q, want %q", got, want)
	}
}
