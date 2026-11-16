package rclone

import (
	"bufio"
	"context"
	"os/exec"
	"runtime"

	"github.com/RainerGewalt/trailtransfer/internal/commands"
	"github.com/RainerGewalt/trailtransfer/internal/logging"
)

// Progress is a running snapshot passed to the progress callback.
type Progress struct {
	FilesTotal       int
	FilesTransferred int
	BytesTotal       int64
	BytesTransferred int64
}

// Outcome is the terminal result of an rclone run.
type Outcome struct {
	FilesTotal       int
	FilesTransferred int
	BytesTotal       int64
	BytesTransferred int64
	ExitCode         int
	Errors           []string
	Canceled         bool
}

const maxErrors = 50

// Run executes rclone for the command as a subprocess (no shell), streams
// progress from its JSON stderr, and returns the terminal outcome. Cancelling
// ctx terminates the process.
func (r *Runner) Run(ctx context.Context, c *commands.Command, dryRun bool, onProgress func(Progress)) (Outcome, error) {
	args, err := BuildArgs(r, c, dryRun)
	if err != nil {
		return Outcome{}, err
	}

	// bindToWorker ties rclone to the thread that starts it; the thread has
	// to live as long as rclone does.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	cmd := exec.CommandContext(ctx, r.RclonePath, args...)
	bindToWorker(cmd)
	cmd.Stdin = nil
	cmd.Stdout = nil // discard; all progress is on stderr as JSON
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Outcome{}, err
	}
	if err := cmd.Start(); err != nil {
		return Outcome{}, err
	}

	var out Outcome
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line, ok := parseLine(sc.Bytes())
		if !ok {
			continue
		}
		if line.Level == "error" && line.Msg != "" && len(out.Errors) < maxErrors {
			out.Errors = append(out.Errors, logging.Redact(line.Msg))
		}
		if line.Stats != nil {
			out.BytesTransferred = line.Stats.Bytes
			out.BytesTotal = line.Stats.TotalBytes
			out.FilesTransferred = line.Stats.Transfers
			out.FilesTotal = line.Stats.TotalTransfers
			if onProgress != nil {
				onProgress(Progress{
					FilesTotal:       out.FilesTotal,
					FilesTransferred: out.FilesTransferred,
					BytesTotal:       out.BytesTotal,
					BytesTransferred: out.BytesTransferred,
				})
			}
		}
	}

	waitErr := cmd.Wait()
	if ctx.Err() == context.Canceled {
		out.Canceled = true
	}
	if cmd.ProcessState != nil {
		out.ExitCode = cmd.ProcessState.ExitCode()
	}
	if out.FilesTotal == 0 && out.FilesTransferred > 0 {
		out.FilesTotal = out.FilesTransferred
	}
	return out, waitErr
}
