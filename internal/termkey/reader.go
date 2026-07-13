package termkey

import (
	"os"
	"time"
)

const escapeContinuationTimeout = 30 * time.Millisecond

// Read returns one terminal key read. If an escape sequence is fragmented by
// the PTY, it briefly waits for the remaining CSI bytes before returning.
func Read(input *os.File, scratch []byte) (string, error) {
	n, err := input.Read(scratch)
	if err != nil || n == 0 {
		return "", err
	}
	data := append([]byte(nil), scratch[:n]...)
	deadline := time.Now().Add(escapeContinuationTimeout)
	for incompleteEscapeSequence(data) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		ready, waitErr := waitReadable(input, remaining)
		if waitErr != nil || !ready {
			break
		}
		n, readErr := input.Read(scratch)
		if n > 0 {
			data = append(data, scratch[:n]...)
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return string(data), nil
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
