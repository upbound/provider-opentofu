/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

package reaper

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// fakeReaper returns a reaper whose process table, clock and reap call are
// controlled by the test. reaped records every PID passed to reap.
func fakeReaper(grace time.Duration, zombies *[]proc, now *time.Time, reaped *[]int) *reaper {
	r := newReaper(logging.NewNopLogger(), grace)
	r.now = func() time.Time { return *now }
	r.listZombies = func() ([]proc, error) { return *zombies, nil }
	r.reap = func(pid int) (bool, syscall.WaitStatus, error) {
		*reaped = append(*reaped, pid)
		remaining := (*zombies)[:0]
		for _, z := range *zombies {
			if z.pid != pid {
				remaining = append(remaining, z)
			}
		}
		*zombies = remaining
		return true, 0, nil
	}
	return r
}

func TestSweepReapsOnlyAfterGracePeriod(t *testing.T) {
	const grace = 30 * time.Second
	now := time.Unix(1000, 0)
	zombies := []proc{{pid: 42, start: 7}}
	var reaped []int
	r := fakeReaper(grace, &zombies, &now, &reaped)

	r.sweep() // first seen
	now = now.Add(grace - time.Second)
	r.sweep()
	if len(reaped) != 0 {
		t.Fatalf("reaped %v before the grace period passed", reaped)
	}

	now = now.Add(time.Second)
	r.sweep()
	if len(reaped) != 1 || reaped[0] != 42 {
		t.Fatalf("expected pid 42 to be reaped after the grace period, got %v", reaped)
	}
	if len(r.seen) != 0 {
		t.Errorf("expected no tracked zombies after reaping, got %v", r.seen)
	}
}

func TestSweepForgetsZombiesReapedByTheirOwner(t *testing.T) {
	const grace = 30 * time.Second
	now := time.Unix(1000, 0)
	zombies := []proc{{pid: 42, start: 7}}
	var reaped []int
	r := fakeReaper(grace, &zombies, &now, &reaped)

	r.sweep()
	zombies = nil // os/exec's Wait collected it
	now = now.Add(time.Second)
	r.sweep()

	if len(r.seen) != 0 {
		t.Errorf("expected the collected zombie to be forgotten, got %v", r.seen)
	}
	if len(reaped) != 0 {
		t.Errorf("expected nothing to be reaped, got %v", reaped)
	}
}

func TestSweepRestartsGracePeriodForReusedPID(t *testing.T) {
	const grace = 30 * time.Second
	now := time.Unix(1000, 0)
	zombies := []proc{{pid: 42, start: 7}}
	var reaped []int
	r := fakeReaper(grace, &zombies, &now, &reaped)

	r.sweep()
	// The old process was collected by its owner and the PID was reused by a
	// new process that has only just become a zombie.
	zombies = []proc{{pid: 42, start: 9}}
	now = now.Add(grace)
	r.sweep()

	if len(reaped) != 0 {
		t.Fatalf("reaped a reused PID before its own grace period passed: %v", reaped)
	}
}

func TestSweepSurvivesListError(t *testing.T) {
	r := newReaper(logging.NewNopLogger(), time.Second)
	r.listZombies = func() ([]proc, error) { return nil, errors.New("boom") }
	r.reap = func(int) (bool, syscall.WaitStatus, error) {
		t.Fatal("reap must not be called when listing fails")
		return false, 0, nil
	}
	r.sweep()
}
