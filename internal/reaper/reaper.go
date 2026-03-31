/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

// Package reaper implements zombie-process reaping for the provider process.
// When the provider is PID 1 (or a subreaper), any grandchild processes that
// finish but whose direct parent has already exited are re-parented to this
// process. Without explicit reaping they accumulate as <defunct> (zombie)
// entries in the process table.
package reaper

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// Start registers this process as a subreaper so that orphaned grandchildren
// are re-parented to it, then launches a background goroutine that reaps them
// as they exit. It returns immediately; reaping continues until ctx is done.
func Start(log logging.Logger) error {
	if err := setSubreaper(); err != nil {
		return err
	}

	go reapLoop(log)
	return nil
}

// reapLoop listens for SIGCHLD and calls waitpid in a non-blocking loop to
// collect every zombie that has been re-parented to us.
func reapLoop(log logging.Logger) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGCHLD)

	for range ch {
		reapAll(log)
	}
}

// reapAll drains all pending zombie children in a tight loop.
func reapAll(log logging.Logger) {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			// No more children to reap (ECHILD) or nothing ready yet.
			return
		}
		log.Debug("Reaped zombie child process", "pid", pid, "exitStatus", ws.ExitStatus())
	}
}
