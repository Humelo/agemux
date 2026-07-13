//go:build windows

package termkey

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func waitReadable(input *os.File, timeout time.Duration) (bool, error) {
	milliseconds := timeout.Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}
	result, err := windows.WaitForSingleObject(windows.Handle(input.Fd()), uint32(milliseconds))
	if err != nil {
		return false, err
	}
	switch result {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, fmt.Errorf("unexpected terminal wait result: %d", result)
	}
}
