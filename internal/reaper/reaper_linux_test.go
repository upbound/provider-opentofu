//go:build linux

/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

package reaper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// TestSetSubreaper verifies that prctl(PR_SET_CHILD_SUBREAPER) succeeds and
// can be repeated.
func TestSetSubreaper(t *testing.T) {
	for i := 0; i < 3; i++ {
		if err := setSubreaper(); err != nil {
			t.Fatalf("setSubreaper() call %d returned unexpected error: %v", i+1, err)
		}
	}
}

// TestCheckChildrenFile verifies that the test kernel provides
// CONFIG_PROC_CHILDREN, which the reaper depends on.
func TestCheckChildrenFile(t *testing.T) {
	if err := checkChildrenFile(); err != nil {
		t.Fatalf("checkChildrenFile() returned unexpected error: %v", err)
	}
}

// TestListZombieChildren verifies that an exited, unwaited child is listed with
// its start time, and that a running child is not.
func TestListZombieChildren(t *testing.T) {
	zombie := forkExec(t, "/bin/true")
	running := forkExec(t, "/bin/sleep", "60")
	t.Cleanup(func() {
		_ = syscall.Kill(running, syscall.SIGKILL)
		waitPID(running)
	})
	waitForZombie(t, zombie)
	t.Cleanup(func() { waitPID(zombie) })

	zombies, err := listZombieChildren()
	if err != nil {
		t.Fatalf("listZombieChildren() returned unexpected error: %v", err)
	}

	_, wantStart, err := readStat(zombie)
	if err != nil {
		t.Fatalf("readStat(%d): %v", zombie, err)
	}
	if !containsPID(zombies, zombie, wantStart) {
		t.Errorf("expected zombie %d (start %d) in %v", zombie, wantStart, zombies)
	}
	for _, z := range zombies {
		if z.pid == running {
			t.Errorf("running child %d must not be listed as a zombie", running)
		}
	}
}

// TestSweepReapsUnownedZombies verifies that zombies nobody waits for, with
// both zero and non-zero exit status, are removed from the process table once
// the grace period has passed.
func TestSweepReapsUnownedZombies(t *testing.T) {
	pids := []int{
		forkExec(t, "/bin/true"),
		forkExec(t, "/bin/false"),
		forkExec(t, "/bin/sleep", "60"),
	}
	if err := syscall.Kill(pids[2], syscall.SIGKILL); err != nil {
		t.Fatalf("failed to send SIGKILL: %v", err)
	}
	for _, pid := range pids {
		waitForZombie(t, pid)
	}

	r := newReaper(logging.NewNopLogger(), time.Minute)
	now := time.Now()
	r.now = func() time.Time { return now }

	r.sweep()
	for _, pid := range pids {
		if !procExists(pid) {
			t.Fatalf("zombie %d was reaped before the grace period passed", pid)
		}
	}

	now = now.Add(time.Minute)
	r.sweep()
	for _, pid := range pids {
		if procExists(pid) {
			t.Errorf("expected zombie %d to be reaped after the grace period", pid)
		}
	}
}

// TestRunDoesNotStealOwnedChildren is the regression test for the race
// reported on upbound/provider-opentofu#134: a reaper that calls wait4(-1)
// collects children that os/exec is waiting for, so cmd.Wait fails with ECHILD
// and the exit status is lost. With the reaper running, every command must
// report its real exit status.
func TestRunDoesNotStealOwnedChildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newReaper(logging.NewNopLogger(), time.Second).run(ctx)

	const workers, runs = 8, 40
	var wg sync.WaitGroup
	errs := make(chan error, workers*runs)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < runs; i++ {
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "echo out; echo err >&2; exit 3")
				out, err := cmd.Output()
				var ee *exec.ExitError
				switch {
				case !errors.As(err, &ee):
					errs <- fmt.Errorf("expected *exec.ExitError, got %w", err)
				case cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 3:
					errs <- fmt.Errorf("expected exit code 3, got %v", cmd.ProcessState)
				case string(out) != "out\n" || string(ee.Stderr) != "err\n":
					errs <- fmt.Errorf("unexpected output %q / stderr %q", out, ee.Stderr)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	lost := 0
	for err := range errs {
		lost++
		if lost <= 5 {
			t.Error(err)
		}
	}
	if lost > 0 {
		t.Fatalf("%d of %d commands lost their exit status", lost, workers*runs)
	}
}

// TestRunReapsOrphanedGrandchild verifies the full path: a grandchild whose
// parent has exited is re-parented to this process (the subreaper) and reaped
// automatically once its grace period has passed.
func TestRunReapsOrphanedGrandchild(t *testing.T) {
	if err := setSubreaper(); err != nil {
		t.Fatalf("setSubreaper(): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go newReaper(logging.NewNopLogger(), 300*time.Millisecond).run(ctx)

	// sh starts a background sleep, prints its PID and exits right away, which
	// orphans the sleep.
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 0.5 >/dev/null 2>&1 & echo $!").Output()
	if err != nil {
		t.Fatalf("failed to start grandchild: %v", err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("unexpected output %q: %v", out, err)
	}

	if ppid, err := readPPID(grandchild); err == nil && ppid != os.Getpid() {
		t.Fatalf("expected grandchild %d to be re-parented to %d, got parent %d", grandchild, os.Getpid(), ppid)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !procExists(grandchild) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("orphaned grandchild %d was not reaped within the deadline", grandchild)
}

// forkExec starts argv as a child that os/exec does not know about, so nothing
// but the reaper (or the test) waits for it.
func forkExec(t *testing.T, argv0 string, args ...string) int {
	t.Helper()
	pid, err := syscall.ForkExec(argv0, append([]string{argv0}, args...), &syscall.ProcAttr{
		Files: []uintptr{0, 1, 2},
	})
	if err != nil {
		t.Fatalf("ForkExec(%s): %v", argv0, err)
	}
	return pid
}

func waitPID(pid int) {
	var ws syscall.WaitStatus
	_, _ = syscall.Wait4(pid, &ws, 0, nil)
}

func procExists(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

func containsPID(ps []proc, pid int, start uint64) bool {
	for _, p := range ps {
		if p.pid == pid && p.start == start {
			return true
		}
	}
	return false
}

// waitForZombie polls /proc/<pid>/stat until the process is a zombie,
// confirming it has exited but has not been waited on.
func waitForZombie(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, _, err := readStat(pid)
		if err != nil {
			t.Fatalf("process %d disappeared before it became a zombie: %v", pid, err)
		}
		if state == 'Z' {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d did not become a zombie within the deadline", pid)
}

// readPPID returns the parent PID of pid from /proc/<pid>/stat.
func readPPID(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	i := strings.LastIndexByte(string(data), ')')
	fields := strings.Fields(string(data[i+1:]))
	if len(fields) < 2 {
		return 0, fmt.Errorf("cannot parse /proc/%d/stat", pid)
	}
	return strconv.Atoi(fields[1])
}
