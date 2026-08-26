/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

package reaper

import (
	"golang.org/x/sys/unix"
)

// setSubreaper uses prctl(PR_SET_CHILD_SUBREAPER) to make the current process
// the subreaper for orphaned grandchildren on Linux.
func setSubreaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}
