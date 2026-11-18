//go:build !linux

package rclone

import "os/exec"

// bindToWorker is a no-op outside Linux: a killed worker can leave rclone
// running there (docs/ARCHITECTURE.md, "Restart and recovery").
func bindToWorker(*exec.Cmd) {}
