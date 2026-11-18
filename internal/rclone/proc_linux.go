//go:build linux

package rclone

import (
	"os/exec"
	"syscall"
)

// bindToWorker makes the kernel end rclone when the worker dies, also when
// the worker is killed and cannot do it itself. Without it a killed worker
// leaves rclone running: the next start would seal the job as interrupted
// while files are still being written.
func bindToWorker(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
