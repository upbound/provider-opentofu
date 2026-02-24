/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package workdir

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/spf13/afero"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/upbound/provider-opentofu/apis/cluster/v1beta1"
	namespacedv1beta1 "github.com/upbound/provider-opentofu/apis/namespaced/v1beta1"
)

// Error strings.
const (
	errListWorkspaces = "cannot list workspaces"
	errFmtReadDir     = "cannot read directory %q"
)

// A GarbageCollector garbage collects the working directories of tofu
// workspaces that no longer exist.
type GarbageCollector struct {
	kube      client.Client
	parentDir string
	fs        afero.Afero
	interval  time.Duration
	log       logging.Logger
}

// A GarbageCollectorOption configures a new GarbageCollector.
type GarbageCollectorOption func(*GarbageCollector)

// WithFs configures the afero filesystem implementation in which work dirs will
// be garbage collected. The default is the real operating system filesystem.
func WithFs(fs afero.Afero) GarbageCollectorOption {
	return func(gc *GarbageCollector) { gc.fs = fs }
}

// WithInterval configures how often garbage collection will run. The default
// interval is one hour.
func WithInterval(i time.Duration) GarbageCollectorOption {
	return func(gc *GarbageCollector) { gc.interval = i }
}

// WithLogger configures the logger that will be used. The default is a no-op
// logger never emits logs.
func WithLogger(l logging.Logger) GarbageCollectorOption {
	return func(gc *GarbageCollector) { gc.log = l }
}

// NewGarbageCollector returns a garbage collector that garbage collects the
// working directories of tofu workspaces.
func NewGarbageCollector(c client.Client, parentDir string, o ...GarbageCollectorOption) *GarbageCollector {
	gc := &GarbageCollector{
		kube:      c,
		parentDir: parentDir,
		fs:        afero.Afero{Fs: afero.NewOsFs()},
		interval:  1 * time.Hour,
		log:       logging.NewNopLogger(),
	}

	for _, fn := range o {
		fn(gc)
	}

	return gc
}

// Start runs the garbage collector. Blocks until the supplied context
// is done.
//
// Implements manager.Runnable to allow controller-runtime
// managers can manage the garbage collector.
func (gc *GarbageCollector) Start(ctx context.Context) error {
	t := time.NewTicker(gc.interval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := gc.collect(ctx); err != nil {
				gc.log.Info("Garbage collection failed", "error", err)
			}
		}
	}
}

func isUUID(u string) bool {
	_, err := uuid.Parse(u)
	return err == nil
}

func (gc *GarbageCollector) collect(ctx context.Context) error { //nolint:gocyclo // easier to follow as a unit
	gc.log.Debug("Running workspace garbage collection", "dir", gc.parentDir)
	exists := map[string]bool{}
	listedAny := false

	// List cluster-scoped workspaces
	// Note: cluster-scoped Workspace CRD may not be available
	// (e.g. disabled via ManagedResourceActivationPolicies)
	clusterList := &clusterv1beta1.WorkspaceList{}
	if err := gc.kube.List(ctx, clusterList); err != nil {
		switch {
		case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
			// cluster-scoped Workspace CRD not installed, so no instances (safe to continue)
			gc.log.Debug("Cluster-scoped Workspace CRD not installed, skipping")
		case apierrors.IsForbidden(err):
			// cluster-scoped Workspaces might still exist, cannot safely determine
			gc.log.Debug("No RBAC permissions to list cluster-scoped workspaces, aborting garbage collection")
			return err
		default:
			// cluster-scoped Workspaces might still exist, cannot safely determine
			gc.log.Debug("Failed to list cluster-scoped workspaces, aborting garbage collection")
			return err
		}
	} else {
		listedAny = true
		for _, ws := range clusterList.Items {
			exists[string(ws.GetUID())] = true
		}
	}

	// List namespaced workspaces
	// Note: namespaced `Workspace` CRD may not be installed
	// (e.g. disabled via ManagedResourceActivationPolicies)
	namespacedList := &namespacedv1beta1.WorkspaceList{}
	if err := gc.kube.List(ctx, namespacedList); err != nil {
		switch {
		case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
			// no workspaces of namespaced type can exist (safe to continue)
			gc.log.Debug("Namespaced Workspace CRD not installed, skipping")
		case apierrors.IsForbidden(err):
			// Namespaced workspaces might exist, log and abort GC
			gc.log.Debug("No RBAC permissions to list namespaced workspaces, aborting garbage collection")
			return err
		default:
			// cannot safely determine whether the workspace, abort GC
			gc.log.Debug("Failed to list namespaced workspaces, aborting garbage collection")
			return err
		}
	} else {
		listedAny = true
		for _, ws := range namespacedList.Items {
			exists[string(ws.GetUID())] = true
		}
	}

	// we reach this path IFF apiserver returned `NotFound` or `NoKindMatchError`
	// for both List calls  of cluster-scoped and namespaced Workspace MRs,
	// i.e. both APIs does not exist.
	//
	// This could potentially happen with a misconfigured
	// ManagedResourceActivationPolicy that disabled both MR APIs.
	// We avoid any GC here just to be safe.
	if !listedAny {
		gc.log.Debug("No Workspace MR APIs available, skipping garbage collection")
		return nil
	}

	fis, err := gc.fs.ReadDir(gc.parentDir)
	if err != nil {
		return errors.Wrapf(err, errFmtReadDir, gc.parentDir)
	}

	failed := make([]string, 0)
	for _, fi := range fis {
		if !fi.IsDir() || !isUUID(fi.Name()) {
			continue
		}
		if exists[fi.Name()] {
			continue
		}
		path := filepath.Join(gc.parentDir, fi.Name())
		if err := gc.fs.RemoveAll(path); err != nil {
			failed = append(failed, path)
		}
	}

	if len(failed) > 0 {
		return errors.Errorf("could not delete directories: %v", strings.Join(failed, ", "))
	}

	return nil
}
