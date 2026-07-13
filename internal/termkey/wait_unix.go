//go:build !windows

package termkey

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func waitReadable(input *os.File, timeout time.Duration) (bool, error) {
	milliseconds := int(timeout.Milliseconds())
	if milliseconds < 1 {
		milliseconds = 1
	}
	fds := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, milliseconds)
	return n > 0 && fds[0].Revents&unix.POLLIN != 0, err
}
