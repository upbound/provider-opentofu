//go:build !linux

/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

package reaper

import "syscall"

// setSubreaper is a no-op on non-Linux platforms.
func setSubreaper() error {
	return nil
}

// checkChildrenFile is a no-op on non-Linux platforms.
func checkChildrenFile() error {
	return nil
}

// listZombieChildren finds nothing on non-Linux platforms; the provider only
// runs as a Linux container.
func listZombieChildren() ([]proc, error) {
	return nil, nil
}

// reapPID is a no-op on non-Linux platforms.
func reapPID(int) (bool, syscall.WaitStatus, error) {
	return false, 0, nil
}
