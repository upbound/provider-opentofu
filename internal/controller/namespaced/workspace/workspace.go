/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"
	"github.com/pkg/errors"
	"github.com/spf13/afero"
	"github.com/upbound/provider-opentofu/internal/clients"
	corev1 "k8s.io/api/core/v1"
	extensionsV1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/hashicorp/go-getter"

	"github.com/upbound/provider-opentofu/apis/namespaced/v1beta1"
	"github.com/upbound/provider-opentofu/internal/features"
	"github.com/upbound/provider-opentofu/internal/opentofu"
	"github.com/upbound/provider-opentofu/internal/workdir"
)

const (
	errNotWorkspace = "managed resource is not a Workspace custom resource"
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errGetCreds     = "cannot get credentials"

	errMkdir           = "cannot make tofu configuration directory"
	errRemoteModule    = "cannot get remote tofu module"
	errSetGitCredDir   = "cannot set GIT_CRED_DIR environment variable"
	errWriteCreds      = "cannot write tofu credentials"
	errWriteGitCreds   = "cannot write .git-credentials to /tmp dir"
	errWriteConfig     = "cannot write tofu configuration " + tfConfig
	errWriteMain       = "cannot write tofu configuration "
	errWriteBackend    = "cannot write tofu configuration " + tfBackendFile
	errInit            = "cannot initialize tofu configuration"
	errWorkspace       = "cannot select tofu workspace"
	errResources       = "cannot list tofu resources"
	errDiff            = "cannot diff (i.e. plan) tofu configuration"
	errOutputs         = "cannot list tofu outputs"
	errOptions         = "cannot determine tofu options"
	errApply           = "cannot apply tofu configuration"
	errDestroy         = "cannot destroy tofu configuration"
	errVarFile         = "cannot get tfvars"
	errVarMap          = "cannot get tfvars from var map"
	errVarResolution   = "cannot resolve variables"
	errDeleteWorkspace = "cannot delete tofu workspace"
	errChecksum        = "cannot calculate workspace checksum"

	gitCredentialsFilename = ".git-credentials"
)

const (
	tofuPath      = "tofu"
	tfMain        = "main.tf"
	tfMainJSON    = "main.tf.json"
	tfConfig      = "crossplane-provider-config.tf"
	tfBackendFile = "crossplane.remote.tfbackend"
)

func envVarFallback(envvar string, fallback string) string {
	if value, ok := os.LookupEnv(envvar); ok {
		return value
	}
	return fallback
}

var tfDir = envVarFallback("XP_TF_DIR", "/tofu")

type tofuclient interface {
	Init(ctx context.Context, o ...opentofu.InitOption) error
	Workspace(ctx context.Context, name string) error
	Outputs(ctx context.Context) ([]opentofu.Output, error)
	Resources(ctx context.Context) ([]string, error)
	Diff(ctx context.Context, o ...opentofu.Option) (bool, error)
	Apply(ctx context.Context, o ...opentofu.Option) error
	Destroy(ctx context.Context, o ...opentofu.Option) error
	DeleteCurrentWorkspace(ctx context.Context) error
	GenerateChecksum(ctx context.Context) (string, error)
}

// Setup adds a controller that reconciles Workspace managed resources.
func Setup(mgr ctrl.Manager, o controller.Options, timeout, pollJitter time.Duration) error {
	name := managed.ControllerName(v1beta1.WorkspaceGroupKind)

	fs := afero.Afero{Fs: afero.NewOsFs()}
	gcWorkspace := workdir.NewGarbageCollector(mgr.GetClient(), tfDir, workdir.WithFs(fs), workdir.WithLogger(o.Logger))
	go gcWorkspace.Run(context.TODO(), true)

	gcTmp := workdir.NewGarbageCollector(mgr.GetClient(), filepath.Join("/tmp", tfDir), workdir.WithFs(fs), workdir.WithLogger(o.Logger))
	go gcTmp.Run(context.TODO(), true)

	c := &connector{
		kube:   mgr.GetClient(),
		usage:  resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1beta1.ProviderConfigUsage{}),
		logger: o.Logger,
		fs:     fs,
		tofu: func(dir string, usePluginCache bool, enableTofuCLILogging bool, logger logging.Logger, envs ...string) tofuclient {
			return opentofu.Harness{Path: tofuPath, Dir: dir, UsePluginCache: usePluginCache, EnableTofuCLILogging: enableTofuCLILogging, Logger: logger, Envs: envs}
		},
	}

	opts := []managed.ReconcilerOption{
		managed.WithPollInterval(o.PollInterval),
		managed.WithPollJitterHook(pollJitter),
		managed.WithExternalConnecter(c),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithTimeout(timeout),
		managed.WithMetricRecorder(o.MetricOptions.MRMetrics),
	}

	if o.Features.Enabled(features.EnableBetaManagementPolicies) {
		opts = append(opts, managed.WithManagementPolicies())
	}

	if err := mgr.Add(statemetrics.NewMRStateRecorder(
		mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1beta1.WorkspaceList{}, o.MetricOptions.PollStateMetricInterval)); err != nil {
		return err
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1beta1.WorkspaceGroupVersionKind),
		opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1beta1.Workspace{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// SetupGated adds a controller that reconciles ProviderConfigs by accounting for
// their current usage.
func SetupGated(mgr ctrl.Manager, o controller.Options, timeout time.Duration, pollJitter time.Duration) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o, timeout, pollJitter); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1beta1.WorkspaceGroupVersionKind.String())
		}
	}, v1beta1.WorkspaceGroupVersionKind)
	return nil
}

type connector struct {
	kube   client.Client
	usage  clients.ModernTracker
	logger logging.Logger
	fs     afero.Afero
	tofu   func(dir string, usePluginCache bool, enableTofuCLILogging bool, logger logging.Logger, envs ...string) tofuclient
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) { //nolint:gocyclo
	// NOTE(negz): This method is slightly over our complexity goal, but I
	// can't immediately think of a clean way to decompose it without
	// affecting readability.

	cr, ok := mg.(*v1beta1.Workspace)
	if !ok {
		return nil, errors.New(errNotWorkspace)
	}
	l := c.logger.WithValues("request", cr.Name)
	// NOTE(negz): This directory will be garbage collected by the workdir
	// garbage collector that is started in Setup.
	dir := filepath.Join(tfDir, string(cr.GetUID()))
	if err := c.fs.MkdirAll(dir, 0700); resource.Ignore(os.IsExist, err) != nil {
		return nil, errors.Wrap(err, errMkdir)
	}
	if err := c.fs.MkdirAll(filepath.Join("/tmp", tfDir), 0700); resource.Ignore(os.IsExist, err) != nil {
		return nil, errors.Wrap(err, errMkdir)
	}

	pc, err := clients.ResolveProviderConfig(ctx, c.kube, nil, c.usage, mg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to resolve provider config")
	}

	// Make git credentials available to inline and remote sources
	for _, cd := range pc.Spec.Credentials {
		if cd.Filename != gitCredentialsFilename {
			continue
		}
		data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
		if err != nil {
			return nil, errors.Wrap(err, errGetCreds)
		}
		// NOTE(bobh66): Put the git credentials file in /tmp/tofu/<UUID> so it doesn't get removed or overwritten
		// by the remote module source case
		gitCredDir := filepath.Clean(filepath.Join("/tmp", dir))
		if err = c.fs.MkdirAll(gitCredDir, 0700); err != nil {
			return nil, errors.Wrap(err, errWriteGitCreds)
		}

		// NOTE(ytsarev): Make go-getter pick up .git-credentials, see /.gitconfig in the container image
		err = os.Setenv("GIT_CRED_DIR", gitCredDir)
		if err != nil {
			return nil, errors.Wrap(err, errSetGitCredDir)
		}
		p := filepath.Clean(filepath.Join(gitCredDir, filepath.Base(cd.Filename)))
		if err := c.fs.WriteFile(p, data, 0600); err != nil {
			return nil, errors.Wrap(err, errWriteGitCreds)
		}
	}

	switch cr.Spec.ForProvider.Source {
	case v1beta1.ModuleSourceRemote:
		gc := getter.Client{
			Src: cr.Spec.ForProvider.Module,
			Dst: dir,
			Pwd: dir,

			Mode: getter.ClientModeDir,
		}
		err := gc.Get()
		if err != nil {
			return nil, errors.Wrap(err, errRemoteModule)
		}

	case v1beta1.ModuleSourceInline:
		fn := tfMain
		if cr.Spec.ForProvider.InlineFormat == v1beta1.FileFormatJSON {
			fn = tfMainJSON
		}
		if err := c.fs.WriteFile(filepath.Join(dir, fn), []byte(cr.Spec.ForProvider.Module), 0600); err != nil {
			return nil, errors.Wrap(err, errWriteMain+fn)
		}
	}

	if len(cr.Spec.ForProvider.Entrypoint) > 0 {
		entrypoint := strings.ReplaceAll(cr.Spec.ForProvider.Entrypoint, "../", "")
		dir = filepath.Join(dir, entrypoint)
	}

	for _, cd := range pc.Spec.Credentials {
		data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
		if err != nil {
			return nil, errors.Wrap(err, errGetCreds)
		}
		p := filepath.Clean(filepath.Join(dir, filepath.Base(cd.Filename)))
		if err := c.fs.WriteFile(p, data, 0600); err != nil {
			return nil, errors.Wrap(err, errWriteCreds)
		}
	}

	if pc.Spec.Configuration != nil {
		if err := c.fs.WriteFile(filepath.Join(dir, tfConfig), []byte(*pc.Spec.Configuration), 0600); err != nil {
			return nil, errors.Wrap(err, errWriteConfig)
		}
	}

	if pc.Spec.BackendFile != nil {
		if err := c.fs.WriteFile(filepath.Join(dir, tfBackendFile), []byte(*pc.Spec.BackendFile), 0600); err != nil {
			return nil, errors.Wrap(err, errWriteBackend)
		}
	}

	if pc.Spec.PluginCache == nil {
		pc.Spec.PluginCache = new(bool)
		*pc.Spec.PluginCache = true
	}

	envs := make([]string, len(cr.Spec.ForProvider.Env))
	for idx, env := range cr.Spec.ForProvider.Env {
		runtimeVal := env.Value
		if runtimeVal == "" {
			switch {
			case env.ConfigMapKeyReference != nil:
				cm := &corev1.ConfigMap{}
				r := env.ConfigMapKeyReference
				nn := types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
				if err := c.kube.Get(ctx, nn, cm); err != nil {
					return nil, errors.Wrap(err, errVarResolution)
				}
				runtimeVal, ok = cm.Data[r.Key]
				if !ok {
					return nil, errors.Wrap(fmt.Errorf("couldn't find key %v in ConfigMap %v/%v", r.Key, r.Namespace, r.Name), errVarResolution)
				}
			case env.SecretKeyReference != nil:
				s := &corev1.Secret{}
				r := env.SecretKeyReference
				nn := types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
				if err := c.kube.Get(ctx, nn, s); err != nil {
					return nil, errors.Wrap(err, errVarResolution)
				}
				secretBytes, ok := s.Data[r.Key]
				if !ok {
					return nil, errors.Wrap(fmt.Errorf("couldn't find key %v in Secret %v/%v", r.Key, r.Namespace, r.Name), errVarResolution)
				}
				runtimeVal = string(secretBytes)
			}
		}
		envs[idx] = strings.Join([]string{env.Name, runtimeVal}, "=")
	}

	tofu := c.tofu(dir, *pc.Spec.PluginCache, cr.Spec.ForProvider.EnableTofuCLILogging, l, envs...)
	if cr.Status.AtProvider.Checksum != "" {
		checksum, err := tofu.GenerateChecksum(ctx)
		if err != nil {
			return nil, errors.Wrap(err, errChecksum)
		}
		if cr.Status.AtProvider.Checksum == checksum {
			l.Debug("Checksums match - skip running tofu init")
			return &external{tofu: tofu, kube: c.kube, logger: c.logger}, errors.Wrap(tofu.Workspace(ctx, meta.GetExternalName(cr)), errWorkspace)
		}
		l.Debug("Checksums don't match so run tofu init:", "old", cr.Status.AtProvider.Checksum, "new", checksum)
	}

	o := make([]opentofu.InitOption, 0, len(cr.Spec.ForProvider.InitArgs))
	if pc.Spec.BackendFile != nil {
		o = append(o, opentofu.WithInitArgs([]string{"-backend-config=" + filepath.Join(dir, tfBackendFile)}))
	}
	o = append(o, opentofu.WithInitArgs(cr.Spec.ForProvider.InitArgs))
	if err := tofu.Init(ctx, o...); err != nil {
		return nil, errors.Wrap(err, errInit)
	}
	return &external{tofu: tofu, kube: c.kube}, errors.Wrap(tofu.Workspace(ctx, meta.GetExternalName(cr)), errWorkspace)
}

type external struct {
	tofu   tofuclient
	kube   client.Client
	logger logging.Logger
}

func (c *external) checkDiff(ctx context.Context, cr *v1beta1.Workspace) (bool, error) {
	o, err := c.options(ctx, cr.Spec.ForProvider)
	if err != nil {
		return false, errors.Wrap(err, errOptions)
	}

	o = append(o, opentofu.WithArgs(cr.Spec.ForProvider.PlanArgs))
	differs, err := c.tofu.Diff(ctx, o...)
	if err != nil {
		if !meta.WasDeleted(cr) {
			return false, errors.Wrap(err, errDiff)
		}
		// tofu plan can fail on deleted resources, so let the reconciliation loop
		// call Delete() if there are still resources in the tfstate file
		differs = false
	}
	return differs, nil
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1beta1.Workspace)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotWorkspace)
	}

	differs, err := c.checkDiff(ctx, cr)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	r, err := c.tofu.Resources(ctx)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errResources)
	}
	if meta.WasDeleted(cr) && len(r) == 0 {
		// The CR was deleted and there are no more tofu resources so the workspace can be deleted
		if err = c.tofu.DeleteCurrentWorkspace(ctx); err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, errDeleteWorkspace)
		}
	}
	// Include any non-sensitive outputs in our status
	op, err := c.tofu.Outputs(ctx)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errOutputs)
	}
	cr.Status.AtProvider = generateWorkspaceObservation(op)

	checksum, err := c.tofu.GenerateChecksum(ctx)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errChecksum)
	}
	cr.Status.AtProvider.Checksum = checksum

	if !differs {
		// TODO(negz): Allow Workspaces to optionally derive their readiness from an
		// output - similar to the logic XRs use to derive readiness from a field of
		// a composed resource.
		cr.Status.SetConditions(xpv1.Available())
	}

	return managed.ExternalObservation{
		ResourceExists:          len(r)+len(op) > 0,
		ResourceUpToDate:        !differs,
		ResourceLateInitialized: false,
		ConnectionDetails:       op2cd(op),
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	// OpenTofu does not have distinct 'create' and 'update' operations.
	u, err := c.Update(ctx, mg)
	return managed.ExternalCreation(u), err
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1beta1.Workspace)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotWorkspace)
	}

	o, err := c.options(ctx, cr.Spec.ForProvider)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errOptions)
	}

	o = append(o, opentofu.WithArgs(cr.Spec.ForProvider.ApplyArgs))
	if err := c.tofu.Apply(ctx, o...); err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errApply)
	}

	op, err := c.tofu.Outputs(ctx)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errOutputs)
	}
	cr.Status.AtProvider = generateWorkspaceObservation(op)
	// TODO(negz): Allow Workspaces to optionally derive their readiness from an
	// output - similar to the logic XRs use to derive readiness from a field of
	// a composed resource.
	// Note that since Create() calls this function the Reconciler will overwrite this Available condition with Creating
	// on the first pass and it will get reset to Available() by Observe() on the next pass if there are no differences.
	// Leave this call for the Update() case.
	cr.Status.SetConditions(xpv1.Available())
	return managed.ExternalUpdate{ConnectionDetails: op2cd(op)}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1beta1.Workspace)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotWorkspace)
	}

	o, err := c.options(ctx, cr.Spec.ForProvider)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, errOptions)
	}

	o = append(o, opentofu.WithArgs(cr.Spec.ForProvider.DestroyArgs))
	return managed.ExternalDelete{}, errors.Wrap(c.tofu.Destroy(ctx, o...), errDestroy)
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

//nolint:gocyclo
func (c *external) options(ctx context.Context, p v1beta1.WorkspaceParameters) ([]opentofu.Option, error) {
	o := make([]opentofu.Option, 0, len(p.Vars)+len(p.VarFiles)+len(p.DestroyArgs)+len(p.ApplyArgs)+len(p.PlanArgs))

	for _, v := range p.Vars {
		o = append(o, opentofu.WithVar(v.Key, v.Value))
	}

	for _, vf := range p.VarFiles {
		fmt := opentofu.HCL
		if vf.Format != nil && *vf.Format == v1beta1.FileFormatJSON {
			fmt = opentofu.JSON
		}

		switch vf.Source {
		case v1beta1.VarFileSourceConfigMapKey:
			cm := &corev1.ConfigMap{}
			r := vf.ConfigMapKeyReference
			nn := types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
			if err := c.kube.Get(ctx, nn, cm); err != nil {
				return nil, errors.Wrap(err, errVarFile)
			}
			o = append(o, opentofu.WithVarFile([]byte(cm.Data[r.Key]), fmt))

		case v1beta1.VarFileSourceSecretKey:
			s := &corev1.Secret{}
			r := vf.SecretKeyReference
			nn := types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
			if err := c.kube.Get(ctx, nn, s); err != nil {
				return nil, errors.Wrap(err, errVarFile)
			}
			o = append(o, opentofu.WithVarFile(s.Data[r.Key], fmt))
		}
	}

	if p.VarMap != nil {
		jsonBytes, err := json.Marshal(p.VarMap)
		if err != nil {
			return nil, errors.Wrap(err, errVarMap)
		}
		o = append(o, opentofu.WithVarFile(jsonBytes, opentofu.JSON))
	}

	return o, nil
}

func op2cd(o []opentofu.Output) managed.ConnectionDetails {
	cd := managed.ConnectionDetails{}
	for _, op := range o {
		if op.Type == opentofu.OutputTypeString {
			cd[op.Name] = []byte(op.StringValue())
			continue
		}
		if j, err := op.JSONValue(); err == nil {
			cd[op.Name] = j
		}
	}
	return cd
}

// generateWorkspaceObservation is used to produce v1beta1.WorkspaceObservation from
// workspace_type.Workspace.
func generateWorkspaceObservation(op []opentofu.Output) v1beta1.WorkspaceObservation {
	wo := v1beta1.WorkspaceObservation{
		Outputs: make(map[string]extensionsV1.JSON, len(op)),
	}
	for _, o := range op {
		if !o.Sensitive {
			if j, err := o.JSONValue(); err == nil {
				wo.Outputs[o.Name] = extensionsV1.JSON{Raw: j}
			}
		}
	}
	return wo
}
