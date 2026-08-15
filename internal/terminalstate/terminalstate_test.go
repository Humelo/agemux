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
	if got, want := output.String(), leaveScreenSequence+MouseResetSequence+KeyboardResetSequence; got != want {
		t.Fatalf("attachment reset = %q, want %q", got, want)
	}
}

func TestGrokClientSetupEnablesAlternateScreenAndMouse(t *testing.T) {
	for _, sequence := range []string{
		"\033[?1049h",
		"\033[?1000h",
		"\033[?1002h",
		"\033[?1003h",
		"\033[?1015h",
		"\033[?1006h",
		"\033[?1004h",
		"\033[?2004h",
		"\033[?25l",
	} {
		if !strings.Contains(GrokClientSetup, sequence) {
			t.Fatalf("Grok attach setup is missing %q", sequence)
		}
	}
	if strings.Contains(GrokClientSetup, "\033[?1049l") {
		t.Fatal("Grok attach setup must not leave the alternate screen")
	}
}

func TestResetAttachmentDisablesMouseTracking(t *testing.T) {
	var output bytes.Buffer
	ResetAttachment(&output)
	got := output.String()
	for _, sequence := range []string{
		"\033[?1000l",
		"\033[?1002l",
		"\033[?1003l",
		"\033[?1015l",
		"\033[?1006l",
	} {
		if !strings.Contains(got, sequence) {
			t.Fatalf("attachment reset is missing %q", sequence)
		}
	}
}
