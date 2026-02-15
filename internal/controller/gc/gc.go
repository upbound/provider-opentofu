/*
SPDX-FileCopyrightText: 2026 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package gc

import (
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/spf13/afero"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/upbound/provider-opentofu/internal/workdir"
)

// Setup initializes and registers the garbage collectors with the manager.
//
// Two GC instances are created:
// - One for the main workspace directory containing workspace roots
// - One for /tmp directory containing temporary workspace files
//
// Each GC queries both cluster-scoped and namespaced workspaces to determine
// which directories can be safely deleted.
func Setup(mgr ctrl.Manager, tfDir string, logger logging.Logger) error {
	fs := afero.Afero{Fs: afero.NewOsFs()}

	// GC for main workspace directory
	gcWorkspace := workdir.NewGarbageCollector(
		mgr.GetClient(),
		tfDir,
		workdir.WithFs(fs),
		workdir.WithLogger(logger),
	)
	if err := mgr.Add(gcWorkspace); err != nil {
		return err
	}

	// GC for temporary workspace directory
	gcTmp := workdir.NewGarbageCollector(
		mgr.GetClient(),
		filepath.Join("/tmp", tfDir),
		workdir.WithFs(fs),
		workdir.WithLogger(logger),
	)
	if err := mgr.Add(gcTmp); err != nil {
		return err
	}

	logger.Debug("Workspace garbage collectors initialized successfully")

	return nil
}
