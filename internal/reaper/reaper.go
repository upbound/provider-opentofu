/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

// Package reaper implements zombie-process reaping for the provider process.
// When the provider is PID 1 (or a subreaper), any grandchild processes that
// finish but whose direct parent has already exited are re-parented to this
// process. Without explicit reaping they accumulate as <defunct> (zombie)
// entries in the process table.
//
// The provider also has children of its own that are owned by os/exec: the
// tofu and sh processes started by internal/opentofu, and the git processes
// that go-getter starts for remote module sources. Their owners call Wait and
// need the exit status, so the reaper must never collect them. It therefore
// never calls wait4(-1). Instead it lists its direct children from
// /proc/self/task/*/children and collects only those that have remained
// zombies for longer than a grace period. os/exec reaps a child as soon as it
// exits, so a child that is still a zombie after the grace period has no owner
// waiting for it.
package reaper

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/pkg/errors"
)

// DefaultGracePeriod is how long a child must have been a zombie before it is
// reaped. It is far longer than the time os/exec needs to collect its own
// children after they exit.
const DefaultGracePeriod = 30 * time.Second

const errSubreaper = "cannot register the provider as child subreaper"

// proc identifies a process. The start time (field 22 of /proc/<pid>/stat)
// distinguishes a reused PID from the process that held it before.
type proc struct {
	pid   int
	start uint64
}

// reaper collects zombie children that nobody else waits for.
type reaper struct {
	log   logging.Logger
	grace time.Duration
	now   func() time.Time

	// listZombies returns the zombie direct children of this process.
	listZombies func() ([]proc, error)
	// reap collects the exit status of pid without blocking.
	reap func(pid int) (bool, syscall.WaitStatus, error)

	// seen records when each zombie was first observed.
	seen map[proc]time.Time
}

func newReaper(log logging.Logger, grace time.Duration) *reaper {
	return &reaper{
		log:         log,
		grace:       grace,
		now:         time.Now,
		listZombies: listZombieChildren,
		reap:        reapPID,
		seen:        map[proc]time.Time{},
	}
}

// Start registers this process as a subreaper so that orphaned grandchildren
// are re-parented to it, verifies that the kernel exposes
// /proc/self/task/<tid>/children (CONFIG_PROC_CHILDREN), and launches a
// background goroutine that reaps orphaned zombies. It returns immediately.
func Start(log logging.Logger) error {
	if err := setSubreaper(); err != nil {
		return errors.Wrap(err, errSubreaper)
	}
	if err := checkChildrenFile(); err != nil {
		return err
	}

	go newReaper(log, DefaultGracePeriod).run(context.Background())
	return nil
}

// run sweeps on every SIGCHLD, and periodically so that zombies get a second
// look once their grace period has passed, until ctx is done.
func (r *reaper) run(ctx context.Context) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGCHLD)
	defer signal.Stop(sig)

	interval := r.grace / 3
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sig:
		case <-t.C:
		}
		r.sweep()
	}
}

// sweep reaps every zombie child that has been a zombie for at least the grace
// period and forgets zombies that have been collected in the meantime.
func (r *reaper) sweep() {
	zombies, err := r.listZombies()
	if err != nil {
		r.log.Debug("Cannot list child processes", "error", err)
		return
	}

	now := r.now()
	current := make(map[proc]struct{}, len(zombies))
	for _, z := range zombies {
		current[z] = struct{}{}

		first, ok := r.seen[z]
		if !ok {
			r.seen[z] = now
			continue
		}
		if now.Sub(first) < r.grace {
			continue
		}

		// A PID is only reused after the kernel's PID allocator wraps around,
		// and this zombie has had no owner for the whole grace period, so the
		// PID still refers to the process identified by z.
		reaped, ws, err := r.reap(z.pid)
		if err != nil {
			r.log.Debug("Cannot reap zombie child process", "pid", z.pid, "error", err)
			continue
		}
		if reaped {
			r.log.Debug("Reaped zombie child process", "pid", z.pid, "exitStatus", ws.ExitStatus())
			delete(r.seen, z)
		}
	}

	// Forget zombies that disappeared, e.g. because their owner reaped them.
	for z := range r.seen {
		if _, ok := current[z]; !ok {
			delete(r.seen, z)
		}
	}
}
