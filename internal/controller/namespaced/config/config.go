/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package config

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/providerconfig"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/upbound/provider-opentofu/apis/namespaced/v1beta1"
)

// Setup adds a controller that reconciles ProviderConfigs by accounting for
// their current usage.
func Setup(mgr ctrl.Manager, o controller.Options, timeout time.Duration, pollJitter time.Duration) error {
	name := providerconfig.ControllerName(v1beta1.ProviderConfigGroupKind)

	of := resource.ProviderConfigKinds{
		Config:    v1beta1.ProviderConfigGroupVersionKind,
		Usage:     v1beta1.ProviderConfigUsageGroupVersionKind,
		UsageList: v1beta1.ProviderConfigUsageListGroupVersionKind,
	}

	r := providerconfig.NewReconciler(mgr, of,
		providerconfig.WithLogger(o.Logger.WithValues("controller", name)),
		providerconfig.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&v1beta1.ProviderConfig{}).
		Watches(&v1beta1.ProviderConfigUsage{}, &resource.EnqueueRequestForProviderConfig{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// SetupGated adds a controller that reconciles ProviderConfigs by accounting for
// their current usage.
func SetupGated(mgr ctrl.Manager, o controller.Options, timeout time.Duration, pollJitter time.Duration) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o, timeout, pollJitter); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1beta1.ProviderConfigGroupVersionKind.String())
		}
	}, v1beta1.ProviderConfigGroupVersionKind, v1beta1.ProviderConfigUsageGroupVersionKind)
	return nil
}
