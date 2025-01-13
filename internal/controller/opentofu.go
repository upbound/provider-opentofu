/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package controller

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/crossplane-runtime/pkg/controller"

	"github.com/upbound/provider-opentofu/internal/controller/config"
	"github.com/upbound/provider-opentofu/internal/controller/workspace"
)

// Setup creates all opentofu controllers with the supplied options and adds
// them to the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options, timeout, pollJitter time.Duration) error {
	if err := config.Setup(mgr, o, timeout); err != nil {
		return err
	}
	if err := workspace.Setup(mgr, o, timeout, pollJitter); err != nil {
		return err
	}
	return nil
}
