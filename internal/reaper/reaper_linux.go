/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

package reaper

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

const errChildrenFile = "cannot access %s: the kernel must be built with CONFIG_PROC_CHILDREN " +
	"(selected by CONFIG_CHECKPOINT_RESTORE) for the provider to reap zombie processes"

// setSubreaper uses prctl(PR_SET_CHILD_SUBREAPER) to make the current process
// the subreaper for orphaned grandchildren on Linux.
func setSubreaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

// checkChildrenFile verifies that the kernel exposes the children of this
// process in /proc, which requires CONFIG_PROC_CHILDREN.
func checkChildrenFile() error {
	p := fmt.Sprintf("/proc/self/task/%d/children", os.Getpid())
	if _, err := os.Stat(p); err != nil {
		return errors.Wrapf(err, errChildrenFile, p)
	}
	return nil
}

// listZombieChildren returns the direct children of this process that are
// zombies. Children are listed per thread, so every task is read. The kernel
// may skip a child that exits while the file is being read; it is picked up by
// the next sweep.
func listZombieChildren() ([]proc, error) {
	files, err := filepath.Glob("/proc/self/task/*/children")
	if err != nil {
		return nil, err
	}

	var zombies []proc
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // path comes from a fixed /proc glob
		if err != nil {
			// The thread may have exited since the glob.
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil {
				continue
			}
			state, start, err := readStat(pid)
			if err != nil || state != 'Z' {
				continue
			}
			zombies = append(zombies, proc{pid: pid, start: start})
		}
	}
	return zombies, nil
}

// readStat returns the state and start time of pid from /proc/<pid>/stat.
func readStat(pid int) (byte, uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	// The format is "pid (comm) state ppid ...". comm may itself contain
	// spaces and parentheses, so parse from the last ')'.
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, errors.Errorf("cannot parse /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[i+1:]))
	// fields[0] is field 3 (state); starttime is field 22.
	const startIdx = 22 - 3
	if len(fields) <= startIdx || len(fields[0]) != 1 {
		return 0, 0, errors.Errorf("cannot parse /proc/%d/stat", pid)
	}
	start, err := strconv.ParseUint(fields[startIdx], 10, 64)
	if err != nil {
		return 0, 0, errors.Wrapf(err, "cannot parse start time in /proc/%d/stat", pid)
	}
	return fields[0][0], start, nil
}

// reapPID collects the exit status of pid without blocking. It reports whether
// the process was reaped.
func reapPID(pid int) (bool, syscall.WaitStatus, error) {
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	if err != nil {
		return false, ws, err
	}
	return wpid == pid, ws, nil
}
