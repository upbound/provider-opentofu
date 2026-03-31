/*
SPDX-FileCopyrightText: 2026 NephoSolutions srl <https://nephosolutions.com>

SPDX-License-Identifier: Apache-2.0
*/

package reaper

import (
	"testing"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// TestReapAllNoChildren verifies that reapAll returns immediately when there
// are no children to reap (WNOHANG behaviour: pid <= 0 on first call).
func TestReapAllNoChildren(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		reapAll(logging.NewNopLogger())
	}()

	select {
	case <-done:
		// passed: reapAll returned without blocking
	case <-time.After(2 * time.Second):
		t.Fatal("reapAll blocked when no children were present")
	}
}

// TestReapAllDoesNotPanic verifies that reapAll does not panic when called
// multiple times in succession on a process with no children.
func TestReapAllDoesNotPanic(t *testing.T) {
	log := logging.NewNopLogger()
	for i := 0; i < 5; i++ {
		reapAll(log)
	}
}
