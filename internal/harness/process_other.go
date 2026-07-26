//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package harness

import "os/exec"

// exec.CommandContext's default cancellation kills the direct process on
// platforms where Precept does not install Unix process-group handling.
func configureCommandCancellation(_ *exec.Cmd) {}
