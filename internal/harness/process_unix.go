//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package harness

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureCommandCancellation(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		// The agent can launch read-only search subprocesses. Kill its entire
		// process group so those children cannot outlive a timeout or Ctrl-C.
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
