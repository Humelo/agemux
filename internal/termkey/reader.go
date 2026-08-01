package termkey

import (
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const escapeContinuationTimeout = 30 * time.Millisecond

type Reader struct {
	input   *os.File
	scratch []byte
	pending []byte
}

func NewReader(input *os.File) *Reader {
	return &Reader{input: input, scratch: make([]byte, 64)}
}

// Read returns exactly one key, retaining additional bytes for the next call.
// PTYs may batch several arrow presses into one read or split one CSI sequence
// across reads, so treating every read as one key loses input in both cases.
func (r *Reader) Read() (string, error) {
	for {
		if key, size, complete := nextKey(r.pending); complete {
			r.pending = r.pending[size:]
			return normalizeKey(string(key)), nil
		}
		if incompleteEscapeSequence(r.pending) {
			if err := r.finishEscapeSequence(); err != nil {
				return "", err
			}
			if incompleteEscapeSequence(r.pending) {
				key := string(r.pending)
				r.pending = nil
				return normalizeKey(key), nil
			}
			continue
		}

		n, err := r.input.Read(r.scratch)
		if n > 0 {
			r.pending = append(r.pending, r.scratch[:n]...)
		}
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", nil
		}
	}
}

func (r *Reader) finishEscapeSequence() error {
	deadline := time.Now().Add(escapeContinuationTimeout)
	for incompleteEscapeSequence(r.pending) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		ready, err := waitReadable(r.input, remaining)
		if err != nil || !ready {
			return err
		}
		n, err := r.input.Read(r.scratch)
		if n > 0 {
			r.pending = append(r.pending, r.scratch[:n]...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Drain discards keys pressed while a non-interactive loading screen is up.
func (r *Reader) Drain() error {
	r.pending = nil
	for {
		ready, err := waitReadable(r.input, time.Millisecond)
		if err != nil || !ready {
			return err
		}
		if _, err := r.input.Read(r.scratch); err != nil {
			return err
		}
	}
}

func nextKey(data []byte) ([]byte, int, bool) {
	if len(data) == 0 {
		return nil, 0, false
	}
	if data[0] != 0x1b {
		if !utf8.FullRune(data) {
			return nil, 0, false
		}
		_, size := utf8.DecodeRune(data)
		return data[:size], size, true
	}
	if len(data) == 1 {
		return data, 1, false
	}
	if data[1] != '[' && data[1] != 'O' {
		return data[:1], 1, true
	}
	for idx, b := range data[2:] {
		if b >= 0x40 && b <= 0x7e {
			size := idx + 3
			return data[:size], size, true
		}
	}
	return data, len(data), false
}

func normalizeKey(key string) string {
	if len(key) == 3 && strings.HasPrefix(key, "\x1bO") && strings.ContainsRune("ABCD", rune(key[2])) {
		return "\x1b[" + key[2:]
	}
	if len(key) < 3 || !strings.HasPrefix(key, "\x1b[") || !strings.ContainsRune("ABCD", rune(key[len(key)-1])) {
		return key
	}
	params := key[2 : len(key)-1]
	if cursorKeyParams(params) {
		return "\x1b[" + key[len(key)-1:]
	}
	return key
}

func cursorKeyParams(params string) bool {
	if params == "" || params == "1" {
		return true
	}
	if !strings.HasPrefix(params, "1;") && !strings.HasPrefix(params, ";") {
		return false
	}
	for _, r := range strings.TrimPrefix(strings.TrimPrefix(params, "1"), ";") {
		if (r < '0' || r > '9') && r != ':' && r != ';' {
			return false
		}
	}
	return true
}

func incompleteEscapeSequence(data []byte) bool {
	if len(data) == 0 || data[0] != 0x1b {
		return false
	}
	if len(data) == 1 {
		return true
	}
	if data[1] != '[' && data[1] != 'O' {
		return false
	}
	for _, b := range data[2:] {
		if b >= 0x40 && b <= 0x7e {
			return false
		}
	}
	return true
}
