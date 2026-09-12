//go:build !linux

/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

package reaper

// setSubreaper is a no-op on non-Linux platforms.
func setSubreaper() error {
	return nil
}
