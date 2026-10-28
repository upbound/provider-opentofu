//go:build linux

/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

package reaper

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// TestSetSubreaper verifies that prctl(PR_SET_CHILD_SUBREAPER) succeeds.
func TestSetSubreaper(t *testing.T) {
	if err := setSubreaper(); err != nil {
		t.Fatalf("setSubreaper() returned unexpected error: %v", err)
	}
}

// TestSetSubreaperIdempotent verifies that calling setSubreaper multiple times
// does not return an error.
func TestSetSubreaperIdempotent(t *testing.T) {
	for i := 0; i < 3; i++ {
		if err := setSubreaper(); err != nil {
			t.Fatalf("setSubreaper() call %d returned unexpected error: %v", i+1, err)
		}
	}
}

// TestReapAllReapsDirectChild verifies that reapAll successfully waits for and
// removes a finished direct child, so that it does not remain as a zombie.
func TestReapAllReapsDirectChild(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start child process: %v", err)
	}
	childPID := cmd.Process.Pid

	// Wait until the child has actually exited (it becomes a zombie at this
	// point because we have not called cmd.Wait yet).
	waitForZombie(t, childPID)

	reapAll(logging.NewNopLogger())

	// After reaping, /proc/<pid> should no longer exist.
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", childPID)); !os.IsNotExist(err) {
		t.Errorf("expected /proc/%d to be gone after reapAll, but it still exists", childPID)
	}
}

// TestReapAllReapsMultipleChildren verifies that reapAll drains multiple zombie
// children in a single call (the internal loop runs until WNOHANG returns
// pid <= 0).
func TestReapAllReapsMultipleChildren(t *testing.T) {
	const childCount = 5
	pids := make([]int, 0, childCount)

	for i := 0; i < childCount; i++ {
		cmd := exec.Command("/bin/true")
		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start child process %d: %v", i, err)
		}
		pids = append(pids, cmd.Process.Pid)
	}

	// Wait for all children to become zombies.
	for _, pid := range pids {
		waitForZombie(t, pid)
	}

	reapAll(logging.NewNopLogger())

	for _, pid := range pids {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !os.IsNotExist(err) {
			t.Errorf("expected /proc/%d to be gone after reapAll, but it still exists", pid)
		}
	}
}

// TestReapAllAfterSIGKILL verifies that reapAll correctly reaps a child that
// was terminated with SIGKILL (non-zero exit status path).
func TestReapAllAfterSIGKILL(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start child process: %v", err)
	}
	childPID := cmd.Process.Pid

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("failed to send SIGKILL: %v", err)
	}

	waitForZombie(t, childPID)

	reapAll(logging.NewNopLogger())

	if _, err := os.Stat(fmt.Sprintf("/proc/%d", childPID)); !os.IsNotExist(err) {
		t.Errorf("expected /proc/%d to be gone after reapAll, but it still exists", childPID)
	}
}

// TestStartReapsChildAfterExit verifies the full Start() flow: the background
// goroutine responds to SIGCHLD and reaps a finished child without any manual
// wait call.
func TestStartReapsChildAfterExit(t *testing.T) {
	if err := Start(logging.NewNopLogger()); err != nil {
		t.Fatalf("Start() returned unexpected error: %v", err)
	}

	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start child process: %v", err)
	}
	childPID := cmd.Process.Pid

	// Poll until the SIGCHLD handler reaps the child or the deadline expires.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", childPID)); os.IsNotExist(err) {
			return // reaped successfully
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Errorf("child process %d was not reaped within the deadline", childPID)
}

// waitForZombie polls /proc/<pid>/status until the process state field is 'Z'
// (zombie), confirming it has exited but not yet been waited on.
func waitForZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, err := readProcessState(pid)
		if err != nil {
			// /proc entry already gone — acceptable but unexpected at this point.
			return
		}
		if state == 'Z' {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d did not become a zombie within the deadline", pid)
}

// readProcessState returns the single-character state byte from the
// "State:" line of /proc/<pid>/status (e.g. 'R', 'S', 'Z').
func readProcessState(pid int) (byte, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	// Each line has the form "Field:\tvalue\n".
	// The State line looks like: "State:\tZ (zombie)"
	start := 0
	for i, b := range data {
		if b != '\n' {
			continue
		}
		line := string(data[start:i])
		start = i + 1
		if len(line) < 7 || line[:6] != "State:" {
			continue
		}
		for _, ch := range line[6:] {
			if ch != '\t' && ch != ' ' {
				return byte(ch), nil
			}
		}
	}
	return 0, fmt.Errorf("State field not found in /proc/%d/status", pid)
}
