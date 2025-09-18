/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package namespaced

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"

	"github.com/upbound/provider-opentofu/internal/controller/namespaced/config"
	"github.com/upbound/provider-opentofu/internal/controller/namespaced/workspace"
)

// Setup creates all opentofu controllers with the supplied logger and adds them
// to the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options, timeout time.Duration, pollJitter time.Duration) error {
	for _, setup := range []func(ctrl.Manager, controller.Options, time.Duration, time.Duration) error{
		config.Setup,
		workspace.Setup,
	} {
		if err := setup(mgr, o, timeout, pollJitter); err != nil {
			return err
		}
	}
	return nil
}

// SetupGated creates all controllers with the supplied logger and adds them to
// the supplied manager gated.
func SetupGated(mgr ctrl.Manager, o controller.Options, timeout time.Duration, pollJitter time.Duration) error {
	for _, setup := range []func(ctrl.Manager, controller.Options, time.Duration, time.Duration) error{
		config.SetupGated,
		workspace.SetupGated,
	} {
		if err := setup(mgr, o, timeout, pollJitter); err != nil {
			return err
		}
	}
	return nil
}
