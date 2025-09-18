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

// Run the garbage collector. Blocks until the supplied context is done.
func (gc *GarbageCollector) Run(ctx context.Context, namespaced bool) {
	t := time.NewTicker(gc.interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := gc.collect(ctx, namespaced); err != nil {
				gc.log.Info("Garbage collection failed", "error", err)
			}
		}
	}
}

func isUUID(u string) bool {
	_, err := uuid.Parse(u)
	return err == nil
}

func (gc *GarbageCollector) collect(ctx context.Context, namespaced bool) error {
	exists := map[string]bool{}

	if namespaced {
		l := &namespacedv1beta1.WorkspaceList{}
		if err := gc.kube.List(ctx, l); err != nil {
			return errors.Wrap(err, errListWorkspaces)
		}
		for _, ws := range l.Items {
			exists[string(ws.GetUID())] = true
		}
	} else {
		l := &clusterv1beta1.WorkspaceList{}
		if err := gc.kube.List(ctx, l); err != nil {
			return errors.Wrap(err, errListWorkspaces)
		}
		for _, ws := range l.Items {
			exists[string(ws.GetUID())] = true
		}
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
