package terminalstate

import (
	"io"
	"os"
	"sync"

	"golang.org/x/term"
)

const (
	// Pop the entire Kitty keyboard-protocol stack. A crashed nested TUI may
	// have pushed more than one entry, so a single CSI < u is not sufficient.
	KeyboardResetSequence = "\033[?1004l\033[?2004l\033[>4;0m\033[<99u"
	CodexKeyboardSetup    = "\033[?2004h\033[>4;0m\033[>7u"
	// Match the DEC private modes Grok emits at TUI startup. shpool's
	// default screen restore replays cells only, so a later attach must
	// put the client into these modes itself.
	GrokClientSetup    = "\033[?1049h\033[?1000h\033[?1002h\033[?1003h\033[?1015h\033[?1006h\033[?1004h\033[?2004h\033[?25l"
	MouseResetSequence = "\033[?1000l\033[?1002l\033[?1003l\033[?1015l\033[?1006l"

	enterScreenSequence = "\033[?25l\033[?1049h"
	leaveScreenSequence = "\033[?1049l\033[?25h"
)

var (
	activeMu sync.Mutex
	active   *Session
)

// Session owns one raw terminal interval. Only one interval is active in an
// agemux process at a time, which also lets signal cleanup restore it safely.
type Session struct {
	input     *os.File
	output    io.Writer
	oldState  *term.State
	alternate bool
	once      sync.Once
}

func BeginScreen(input *os.File, output io.Writer) (*Session, error) {
	return begin(input, output, true)
}

func BeginRaw(input *os.File, output io.Writer) (*Session, error) {
	return begin(input, output, false)
}

func begin(input *os.File, output io.Writer, alternate bool) (*Session, error) {
	Reset(output)
	activeMu.Lock()
	oldState, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		activeMu.Unlock()
		return nil, err
	}
	session := &Session{
		input:     input,
		output:    output,
		oldState:  oldState,
		alternate: alternate,
	}
	active = session
	activeMu.Unlock()
	if alternate {
		_, _ = io.WriteString(output, enterScreenSequence)
	}
	return session, nil
}

func (s *Session) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.alternate {
			_, _ = io.WriteString(s.output, leaveScreenSequence)
		}
		Reset(s.output)
		if s.oldState != nil {
			_ = term.Restore(int(s.input.Fd()), s.oldState)
		}
		activeMu.Lock()
		if active == s {
			active = nil
		}
		activeMu.Unlock()
	})
}

func RestoreActive() {
	activeMu.Lock()
	session := active
	activeMu.Unlock()
	if session != nil {
		session.Close()
	}
}

func Reset(output io.Writer) {
	_, _ = io.WriteString(output, KeyboardResetSequence)
}

func ResetAttachment(output io.Writer) {
	_, _ = io.WriteString(output, leaveScreenSequence)
	_, _ = io.WriteString(output, MouseResetSequence)
	Reset(output)
}

func PrepareAttachment(output io.Writer) {
	ResetAttachment(output)
	_, _ = io.WriteString(output, "\r\n")
}
