/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	"github.com/spf13/afero"
	corev1 "k8s.io/api/core/v1"
	extensionsV1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/upbound/provider-opentofu/apis/namespaced/v1beta1"
	"github.com/upbound/provider-opentofu/internal/opentofu"
)

const (
	tfChecksum = "checksum"
)

type ErrFs struct {
	afero.Fs

	errs map[string]error
}

func (e *ErrFs) MkdirAll(path string, perm os.FileMode) error {
	if err := e.errs[path]; err != nil {
		return err
	}
	return e.Fs.MkdirAll(path, perm)
}

// Called by afero.WriteFile
func (e *ErrFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if err := e.errs[name]; err != nil {
		return nil, err
	}
	return e.Fs.OpenFile(name, flag, perm)
}

type MockTofu struct {
	MockInit                   func(ctx context.Context, o ...opentofu.InitOption) error
	MockWorkspace              func(ctx context.Context, name string) error
	MockOutputs                func(ctx context.Context) ([]opentofu.Output, error)
	MockResources              func(ctx context.Context) ([]string, error)
	MockDiff                   func(ctx context.Context, o ...opentofu.Option) (bool, error)
	MockApply                  func(ctx context.Context, o ...opentofu.Option) error
	MockDestroy                func(ctx context.Context, o ...opentofu.Option) error
	MockDeleteCurrentWorkspace func(ctx context.Context) error
	MockGenerateChecksum       func(ctx context.Context) (string, error)
}

func (tf *MockTofu) Init(ctx context.Context, o ...opentofu.InitOption) error {
	return tf.MockInit(ctx, o...)
}

func (tf *MockTofu) GenerateChecksum(ctx context.Context) (string, error) {
	return tf.MockGenerateChecksum(ctx)
}

func (tf *MockTofu) Workspace(ctx context.Context, name string) error {
	return tf.MockWorkspace(ctx, name)
}

func (tf *MockTofu) Outputs(ctx context.Context) ([]opentofu.Output, error) {
	return tf.MockOutputs(ctx)
}

func (tf *MockTofu) Resources(ctx context.Context) ([]string, error) {
	return tf.MockResources(ctx)
}

func (tf *MockTofu) Diff(ctx context.Context, o ...opentofu.Option) (bool, error) {
	return tf.MockDiff(ctx, o...)
}

func (tf *MockTofu) Apply(ctx context.Context, o ...opentofu.Option) error {
	return tf.MockApply(ctx, o...)
}

func (tf *MockTofu) Destroy(ctx context.Context, o ...opentofu.Option) error {
	return tf.MockDestroy(ctx, o...)
}

func (tf *MockTofu) DeleteCurrentWorkspace(ctx context.Context) error {
	return tf.MockDeleteCurrentWorkspace(ctx)
}

func TestConnect(t *testing.T) {
	errBoom := errors.New("boom")
	uid := types.UID("no-you-id")
	tfCreds := "credentials"

	type fields struct {
		kube  client.Client
		usage resource.Tracker
		fs    afero.Afero
		tofu  func(dir string, usePluginCache bool, enableTofuCLILogging bool, logger logging.Logger, envs ...string) tofuclient
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   error
	}{
		"NotWorkSpaceError": {
			reason: "We should return an error if the supplied managed resource is not a Workspace",
			fields: fields{},
			args: args{
				mg: nil,
			},
			want: errors.New(errNotWorkspace),
		},
		"MakeDirError": {
			reason: "We should return any error encountered while making a directory for our configuration",
			fields: fields{
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(tfDir, string(uid)): errBoom},
					},
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
				},
			},
			want: errors.Wrap(errBoom, errMkdir),
		},
		"TrackUsageError": {
			reason: "We should return any error encountered while tracking ProviderConfig usage",
			fields: fields{
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return errBoom }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
				},
			},
			want: errors.Wrap(errBoom, errTrackPCUsage),
		},
		"GetProviderConfigError": {
			reason: "We should return any error encountered while getting our ProviderConfig",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(errBoom),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errGetPC),
		},
		"GetProviderConfigCredentialsError": {
			reason: "We should return any error encountered while getting our ProviderConfig credentials",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1beta1.ProviderConfig); ok {
							// We're testing through CommonCredentialsExtractor
							// here. We cause an error to be returned by asking
							// for credentials from the environment, but not
							// specifying an environment variable.
							pc.Spec.Credentials = []v1beta1.ProviderCredentials{{
								Source: xpv1.CredentialsSourceEnvironment,
							}}
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errors.New("cannot extract from environment variable when none specified"), errGetCreds),
		},
		"WriteProviderConfigCredentialsError": {
			reason: "We should return any error encountered while writing our ProviderConfig credentials to a file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1beta1.ProviderConfig); ok {
							pc.Spec.Credentials = []v1beta1.ProviderCredentials{{
								Filename: tfCreds,
								Source:   xpv1.CredentialsSourceNone,
							}}
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(tfDir, string(uid), tfCreds): errBoom},
					},
				},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteCreds),
		},
		"WriteProviderConfigCredentialsEntrypointError": {
			reason: "We should return any error encountered while writing our ProviderConfig credentials to a file with entrypoint subdir",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1beta1.ProviderConfig); ok {
							pc.Spec.Credentials = []v1beta1.ProviderCredentials{{
								Filename: tfCreds,
								Source:   xpv1.CredentialsSourceNone,
							}}
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(tfDir, string(uid), "subdir", tfCreds): errBoom},
					},
				},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1beta1.WorkspaceParameters{
							Module:     "I'm HCL!",
							Source:     v1beta1.ModuleSourceInline,
							Entrypoint: "subdir",
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteCreds),
		},
		"WriteProviderGitCredentialsError": {
			reason: "We should return any error encountered while writing our git credentials to a file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1beta1.ProviderConfig); ok {
							pc.Spec.Credentials = []v1beta1.ProviderCredentials{{
								Filename: ".git-credentials",
								Source:   xpv1.CredentialsSourceNone,
							}}
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join("/tmp", tfDir, string(uid), ".git-credentials"): errBoom},
					},
				},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1beta1.WorkspaceParameters{
							Module: "github.com/crossplane/rocks",
							Source: v1beta1.ModuleSourceRemote,
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteGitCreds),
		},
		"WriteProviderGitCredentialsMkdirError": {
			reason: "We should return any error encountered while creating the credentials directory in /tmp",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1beta1.ProviderConfig); ok {
							pc.Spec.Credentials = []v1beta1.ProviderCredentials{{
								Filename: ".git-credentials",
								Source:   xpv1.CredentialsSourceNone,
							}}
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join("/tmp", tfDir, string(uid)): errBoom},
					},
				},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1beta1.WorkspaceParameters{
							Module: "github.com/crossplane/rocks",
							Source: v1beta1.ModuleSourceRemote,
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteGitCreds),
		},
		"WriteConfigError": {
			reason: "We should return any error encountered while writing our crossplane-provider-config.tofu file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1beta1.ProviderConfig); ok {
							cfg := "I'm HCL!"
							pc.Spec.Configuration = &cfg
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(tfDir, string(uid), tfConfig): errBoom},
					},
				},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1beta1.WorkspaceParameters{
							Module: "I'm HCL!",
							Source: v1beta1.ModuleSourceInline,
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteConfig),
		},
		"WriteConfigEntrypointError": {
			reason: "We should return any error encountered while writing our crossplane-provider-config.tofu file to entrypoint subdir location",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1beta1.ProviderConfig); ok {
							cfg := "I'm HCL!"
							pc.Spec.Configuration = &cfg
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(tfDir, string(uid), "subdir", tfConfig): errBoom},
					},
				},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1beta1.WorkspaceParameters{
							Module:     "I'm HCL!",
							Source:     v1beta1.ModuleSourceInline,
							Entrypoint: "subdir",
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteConfig),
		},
		"WriteMainError": {
			reason: "We should return any error encountered while writing our main.tofu file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(tfDir, string(uid), tfMain): errBoom},
					},
				},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1beta1.WorkspaceParameters{
							Module: "I'm HCL!",
							Source: v1beta1.ModuleSourceInline,
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteMain+tfMain),
		},
		"WriteMainJsonError": {
			reason: "We should return any error encountered while writing our main.tofu file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(tfDir, string(uid), tfMainJSON): errBoom},
					},
				},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1beta1.WorkspaceParameters{
							Module:       "I'm JSON!",
							Source:       v1beta1.ModuleSourceInline,
							InlineFormat: v1beta1.FileFormatJSON,
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteMain+tfMainJSON),
		},
		"TofuInitError": {
			reason: "We should return any error encountered while initializing tofu",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{MockInit: func(_ context.Context, _ ...opentofu.InitOption) error { return errBoom }}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errInit),
		},
		"TofuWorkspaceError": {
			reason: "We should return any error encountered while selecting a tofu workspace",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit:      func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
						MockWorkspace: func(_ context.Context, _ string) error { return errBoom },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWorkspace),
		},
		"GenerateChecksumError": {
			reason: "We should return any error when generating the workspace checksum",
			fields: fields{kube: &test.MockClient{
				MockGet: test.NewMockGetFn(nil),
			},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockGenerateChecksum: func(ctx context.Context) (string, error) { return "", errBoom },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1beta1.WorkspaceParameters{
							Module: "I'm HCL!",
							Source: v1beta1.ModuleSourceInline,
						},
					},
					Status: v1beta1.WorkspaceStatus{
						AtProvider: v1beta1.WorkspaceObservation{
							Checksum: tfChecksum,
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errChecksum),
		},
		"ChecksumMatches": {
			reason: "We should return any error when generating the workspace checksum",
			fields: fields{kube: &test.MockClient{
				MockGet: test.NewMockGetFn(nil),
			},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockGenerateChecksum: func(ctx context.Context) (string, error) { return tfChecksum, nil },
						MockWorkspace:        func(_ context.Context, _ string) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1beta1.WorkspaceParameters{
							Module: "I'm HCL!",
							Source: v1beta1.ModuleSourceInline,
						},
					},
					Status: v1beta1.WorkspaceStatus{
						AtProvider: v1beta1.WorkspaceObservation{
							Checksum: tfChecksum,
						},
					},
				},
			},
			want: nil,
		},
		"Success": {
			reason: "We should not return an error when we successfully 'connect' to tofu",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit:             func(ctx context.Context, o ...opentofu.InitOption) error { return nil },
						MockGenerateChecksum: func(ctx context.Context) (string, error) { return tfChecksum, nil },
						MockWorkspace:        func(_ context.Context, _ string) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							InitArgs: []string{"-upgrade=true"},
						},
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: nil,
		},
		"SuccessUsingBackendFile": {
			reason: "We should not return an error when we successfully 'connect' to tofu using a Backend file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1beta1.ProviderConfig); ok {
							cfg := "I'm HCL!"
							backendFile := "I'm a backend!"
							pc.Spec.Configuration = &cfg
							pc.Spec.BackendFile = &backendFile
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				tofu: func(_ string, _ bool, _ bool, _ logging.Logger, _ ...string) tofuclient {
					return &MockTofu{
						MockInit: func(ctx context.Context, o ...opentofu.InitOption) error {
							args := opentofu.InitArgsToString(o)
							if len(args) != 2 {
								return errors.New("two init args are expected")
							} else if args[0] != "-backend-config=/tofu/no-you-id/crossplane.remote.tfbackend" {
								return errors.Errorf("backend config arg has invalid value: %s", args[0])
							}
							return nil
						},
						MockGenerateChecksum: func(ctx context.Context) (string, error) { return tfChecksum, nil },
						MockWorkspace:        func(_ context.Context, _ string) error { return nil },
					}
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							InitArgs: []string{"-upgrade=true"},
						},
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := connector{
				kube:   tc.fields.kube,
				usage:  tc.fields.usage,
				fs:     tc.fields.fs,
				tofu:   tc.fields.tofu,
				logger: logging.NewNopLogger(),
			}
			_, err := c.Connect(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Connect(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestObserve(t *testing.T) {
	errBoom := errors.New("boom")
	now := metav1.Now()
	type fields struct {
		tofu tofuclient
		kube client.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalObservation
		wo  v1beta1.WorkspaceObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NotAWorkspaceError": {
			reason: "We should return an error if the supplied managed resource is not a Workspace",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotWorkspace),
			},
		},
		"GetConfigMapError": {
			reason: "We should return any error we encounter getting tfvars from a ConfigMap",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if _, ok := obj.(*corev1.ConfigMap); ok {
							return errBoom
						}
						return nil
					}),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							VarFiles: []v1beta1.VarFile{
								{
									Source:                v1beta1.VarFileSourceConfigMapKey,
									ConfigMapKeyReference: &v1beta1.KeyReference{},
								},
							},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errors.Wrap(errBoom, errVarFile), errOptions),
			},
		},
		"GetSecretError": {
			reason: "We should return any error we encounter getting tfvars from a Secret",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if _, ok := obj.(*corev1.Secret); ok {
							return errBoom
						}
						return nil
					}),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							VarFiles: []v1beta1.VarFile{
								{
									Source:             v1beta1.VarFileSourceSecretKey,
									SecretKeyReference: &v1beta1.KeyReference{},
								},
							},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errors.Wrap(errBoom, errVarFile), errOptions),
			},
		},
		"GetVarMapError": {
			reason: "We should return any error we encounter getting tfvars from varmap",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if _, ok := obj.(*corev1.Secret); ok {
							return errBoom
						}
						return nil
					}),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							VarMap: &runtime.RawExtension{
								Raw: []byte("I'm not JSON"),
							},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errors.Wrap(errors.New("json: error calling MarshalJSON for type *runtime.RawExtension: invalid character 'I' looking for beginning of value"), errVarMap), errOptions),
			},
		},
		"DiffError": {
			reason: "We should return any error encountered while diffing the tofu configuration",
			fields: fields{
				tofu: &MockTofu{
					MockDiff: func(ctx context.Context, o ...opentofu.Option) (bool, error) { return false, errBoom },
				},
			},
			args: args{
				mg: &v1beta1.Workspace{},
			},
			want: want{
				err: errors.Wrap(errBoom, errDiff),
			},
		},
		"DiffErrorDeletedWithExistingResources": {
			reason: "We should return ResourceUpToDate true when resource is deleted and there are existing resources but tofu plan fails",
			fields: fields{
				tofu: &MockTofu{
					MockDiff:             func(ctx context.Context, o ...opentofu.Option) (bool, error) { return false, errBoom },
					MockGenerateChecksum: func(ctx context.Context) (string, error) { return tfChecksum, nil },
					MockOutputs:          func(ctx context.Context) ([]opentofu.Output, error) { return nil, nil },
					MockResources: func(ctx context.Context) ([]string, error) {
						return []string{"cool_resource.very"}, nil
					},
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: &now,
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: managed.ConnectionDetails{},
				},
				wo: v1beta1.WorkspaceObservation{
					Checksum: tfChecksum,
					Outputs:  map[string]extensionsV1.JSON{},
				},
			},
		},
		"DiffErrorDeletedWithoutExistingResources": {
			reason: "We should return ResourceUpToDate true when resource is deleted and there are no existing resources and tofu plan fails",
			fields: fields{
				tofu: &MockTofu{
					MockDiff:                   func(ctx context.Context, o ...opentofu.Option) (bool, error) { return false, errBoom },
					MockGenerateChecksum:       func(ctx context.Context) (string, error) { return tfChecksum, nil },
					MockOutputs:                func(ctx context.Context) ([]opentofu.Output, error) { return nil, nil },
					MockResources:              func(ctx context.Context) ([]string, error) { return nil, nil },
					MockDeleteCurrentWorkspace: func(ctx context.Context) error { return nil },
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: &now,
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    false,
					ResourceUpToDate:  true,
					ConnectionDetails: managed.ConnectionDetails{},
				},
				wo: v1beta1.WorkspaceObservation{
					Checksum: tfChecksum,
					Outputs:  map[string]extensionsV1.JSON{},
				},
			},
		},
		"DiffErrorDeletedWithoutExistingResourcesWorkspaceDeleteError": {
			reason: "We should return ResourceUpToDate true when resource is deleted and there are no existing resources and tofu plan fails",
			fields: fields{
				tofu: &MockTofu{
					MockDiff:                   func(ctx context.Context, o ...opentofu.Option) (bool, error) { return false, errBoom },
					MockResources:              func(ctx context.Context) ([]string, error) { return nil, nil },
					MockDeleteCurrentWorkspace: func(ctx context.Context) error { return errBoom },
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: &now,
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errDeleteWorkspace),
			},
		},
		"ResourcesError": {
			reason: "We should return any error encountered while listing extant tofu resources",
			fields: fields{
				tofu: &MockTofu{
					MockDiff:      func(ctx context.Context, o ...opentofu.Option) (bool, error) { return false, nil },
					MockResources: func(ctx context.Context) ([]string, error) { return nil, errBoom },
				},
			},
			args: args{
				mg: &v1beta1.Workspace{},
			},
			want: want{
				err: errors.Wrap(errBoom, errResources),
			},
		},
		"OutputsError": {
			reason: "We should return any error encountered while listing tofu outputs",
			fields: fields{
				tofu: &MockTofu{
					MockDiff:      func(ctx context.Context, o ...opentofu.Option) (bool, error) { return false, nil },
					MockResources: func(ctx context.Context) ([]string, error) { return nil, nil },
					MockOutputs:   func(ctx context.Context) ([]opentofu.Output, error) { return nil, errBoom },
				},
			},
			args: args{
				mg: &v1beta1.Workspace{},
			},
			want: want{
				err: errors.Wrap(errBoom, errOutputs),
			},
		},
		"WorkspaceDoesNotExist": {
			reason: "A workspace with zero resources should be considered to be non-existent",
			fields: fields{
				tofu: &MockTofu{
					MockDiff:             func(ctx context.Context, o ...opentofu.Option) (bool, error) { return false, nil },
					MockGenerateChecksum: func(ctx context.Context) (string, error) { return tfChecksum, nil },
					MockResources:        func(ctx context.Context) ([]string, error) { return []string{}, nil },
					MockOutputs:          func(ctx context.Context) ([]opentofu.Output, error) { return nil, nil },
				},
			},
			args: args{
				mg: &v1beta1.Workspace{},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    false,
					ResourceUpToDate:  true,
					ConnectionDetails: managed.ConnectionDetails{},
				},
				wo: v1beta1.WorkspaceObservation{
					Checksum: tfChecksum,
					Outputs:  map[string]extensionsV1.JSON{},
				},
			},
		},
		"WorkspaceExists": {
			reason: "A workspace with resources should return its outputs as connection details",
			fields: fields{
				tofu: &MockTofu{
					MockDiff:             func(ctx context.Context, o ...opentofu.Option) (bool, error) { return false, nil },
					MockGenerateChecksum: func(ctx context.Context) (string, error) { return tfChecksum, nil },
					MockResources: func(ctx context.Context) ([]string, error) {
						return []string{"cool_resource.very"}, nil
					},
					MockOutputs: func(ctx context.Context) ([]opentofu.Output, error) {
						return []opentofu.Output{
							{Name: "string", Type: opentofu.OutputTypeString, Sensitive: false},
							{Name: "object", Type: opentofu.OutputTypeObject, Sensitive: true},
						}, nil
					},
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							PlanArgs: []string{"-refresh=false"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
					ConnectionDetails: managed.ConnectionDetails{
						"string": {},
						"object": []byte("null"), // Because we JSON decode the the value, which is any{}
					},
				},
				wo: v1beta1.WorkspaceObservation{
					Checksum: tfChecksum,
					Outputs: map[string]extensionsV1.JSON{
						"string": {Raw: []byte("null")},
					},
				},
			},
		},
		"WorkspaceExistsOnlyOutputs": {
			reason: "A workspace with only outputs and no resources should set ResourceExists to true",
			fields: fields{
				tofu: &MockTofu{
					MockDiff:             func(ctx context.Context, o ...opentofu.Option) (bool, error) { return false, nil },
					MockGenerateChecksum: func(ctx context.Context) (string, error) { return tfChecksum, nil },
					MockResources: func(ctx context.Context) ([]string, error) {
						return nil, nil
					},
					MockOutputs: func(ctx context.Context) ([]opentofu.Output, error) {
						return []opentofu.Output{
							{Name: "string", Type: opentofu.OutputTypeString, Sensitive: false},
							{Name: "object", Type: opentofu.OutputTypeObject, Sensitive: true},
						}, nil
					},
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							PlanArgs: []string{"-refresh=false"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
					ConnectionDetails: managed.ConnectionDetails{
						"string": {},
						"object": []byte("null"), // Because we JSON decode the the value, which is any{}
					},
				},
				wo: v1beta1.WorkspaceObservation{
					Checksum: tfChecksum,
					Outputs: map[string]extensionsV1.JSON{
						"string": {Raw: []byte("null")},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{tofu: tc.fields.tofu, kube: tc.fields.kube, logger: logging.NewNopLogger()}
			got, err := e.Observe(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
			if tc.args.mg != nil {
				if diff := cmp.Diff(tc.want.wo, tc.args.mg.(*v1beta1.Workspace).Status.AtProvider); diff != "" {
					t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
				}
			}
		})
	}
}

func TestCreate(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		tofu tofuclient
		kube client.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		c   managed.ExternalCreation
		wo  v1beta1.WorkspaceObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NotAWorkspaceError": {
			reason: "We should return an error if the supplied managed resource is not a Workspace",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotWorkspace),
			},
		},
		"GetConfigMapError": {
			reason: "We should return any error we encounter getting tfvars from a ConfigMap",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if _, ok := obj.(*corev1.ConfigMap); ok {
							return errBoom
						}
						return nil
					}),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							VarFiles: []v1beta1.VarFile{
								{
									Source:                v1beta1.VarFileSourceConfigMapKey,
									ConfigMapKeyReference: &v1beta1.KeyReference{},
								},
							},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errors.Wrap(errBoom, errVarFile), errOptions),
			},
		},
		"GetSecretError": {
			reason: "We should return any error we encounter getting tfvars from a Secret",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if _, ok := obj.(*corev1.Secret); ok {
							return errBoom
						}
						return nil
					}),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							VarFiles: []v1beta1.VarFile{
								{
									Source:             v1beta1.VarFileSourceSecretKey,
									SecretKeyReference: &v1beta1.KeyReference{},
								},
							},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errors.Wrap(errBoom, errVarFile), errOptions),
			},
		},
		"GetVarMapError": {
			reason: "We should return any error we encounter getting tfvars from varmap",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if _, ok := obj.(*corev1.Secret); ok {
							return errBoom
						}
						return nil
					}),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							VarMap: &runtime.RawExtension{
								Raw: []byte("I'm not JSON"),
							},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errors.Wrap(errors.New("json: error calling MarshalJSON for type *runtime.RawExtension: invalid character 'I' looking for beginning of value"), errVarMap), errOptions),
			},
		},
		"ApplyError": {
			reason: "We should return any error we encounter applying our tofu configuration",
			fields: fields{
				tofu: &MockTofu{
					MockApply: func(_ context.Context, _ ...opentofu.Option) error { return errBoom },
				},
			},
			args: args{
				mg: &v1beta1.Workspace{},
			},
			want: want{
				err: errors.Wrap(errBoom, errApply),
			},
		},
		"OutputsError": {
			reason: "We should return any error we encounter getting our tofu outputs",
			fields: fields{
				tofu: &MockTofu{
					MockApply:   func(_ context.Context, _ ...opentofu.Option) error { return nil },
					MockOutputs: func(ctx context.Context) ([]opentofu.Output, error) { return nil, errBoom },
				},
			},
			args: args{
				mg: &v1beta1.Workspace{},
			},
			want: want{
				err: errors.Wrap(errBoom, errOutputs),
			},
		},
		"Success": {
			reason: "We should refresh our connection details with any updated outputs after successfully applying the tofu configuration",
			fields: fields{
				tofu: &MockTofu{
					MockApply:            func(_ context.Context, _ ...opentofu.Option) error { return nil },
					MockGenerateChecksum: func(ctx context.Context) (string, error) { return tfChecksum, nil },
					MockOutputs: func(ctx context.Context) ([]opentofu.Output, error) {
						return []opentofu.Output{
							{Name: "string", Type: opentofu.OutputTypeString, Sensitive: true},
							{Name: "object", Type: opentofu.OutputTypeObject, Sensitive: false},
						}, nil
					},
				},
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							ApplyArgs: []string{"-refresh=false"},
							Vars:      []v1beta1.Var{{Key: "super", Value: "cool"}},
							VarFiles: []v1beta1.VarFile{
								{
									Source:                v1beta1.VarFileSourceConfigMapKey,
									ConfigMapKeyReference: &v1beta1.KeyReference{},
								},
								{
									Source:             v1beta1.VarFileSourceSecretKey,
									SecretKeyReference: &v1beta1.KeyReference{},
									Format:             &v1beta1.FileFormatJSON,
								},
							},
						},
					},
				},
			},
			want: want{
				c: managed.ExternalCreation{
					ConnectionDetails: managed.ConnectionDetails{
						"string": {},
						"object": []byte("null"), // Because we JSON decode the value, which is any{}
					},
				},
				wo: v1beta1.WorkspaceObservation{
					Outputs: map[string]extensionsV1.JSON{
						"object": {Raw: []byte("null")},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{tofu: tc.fields.tofu, kube: tc.fields.kube, logger: logging.NewNopLogger()}
			got, err := e.Create(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.c, got); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want, +got:\n%s\n", tc.reason, diff)
			}
			if tc.args.mg != nil {
				if diff := cmp.Diff(tc.want.wo, tc.args.mg.(*v1beta1.Workspace).Status.AtProvider); diff != "" {
					t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
				}
			}
		})
	}
}

func TestDelete(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		tofu tofuclient
		kube client.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   error
	}{
		"NotAWorkspaceError": {
			reason: "We should return an error if the supplied managed resource is not a Workspace",
			args: args{
				mg: nil,
			},
			want: errors.New(errNotWorkspace),
		},
		"GetConfigMapError": {
			reason: "We should return any error we encounter getting tfvars from a ConfigMap",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if _, ok := obj.(*corev1.ConfigMap); ok {
							return errBoom
						}
						return nil
					}),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							VarFiles: []v1beta1.VarFile{
								{
									Source:                v1beta1.VarFileSourceConfigMapKey,
									ConfigMapKeyReference: &v1beta1.KeyReference{},
								},
							},
						},
					},
				},
			},
			want: errors.Wrap(errors.Wrap(errBoom, errVarFile), errOptions),
		},
		"GetSecretError": {
			reason: "We should return any error we encounter getting tfvars from a Secret",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if _, ok := obj.(*corev1.Secret); ok {
							return errBoom
						}
						return nil
					}),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							VarFiles: []v1beta1.VarFile{
								{
									Source:             v1beta1.VarFileSourceSecretKey,
									SecretKeyReference: &v1beta1.KeyReference{},
								},
							},
						},
					},
				},
			},
			want: errors.Wrap(errors.Wrap(errBoom, errVarFile), errOptions),
		},
		"GetVarMapError": {
			reason: "We should return any error we encounter getting tfvars from varmap",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if _, ok := obj.(*corev1.Secret); ok {
							return errBoom
						}
						return nil
					}),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							VarMap: &runtime.RawExtension{
								Raw: []byte("I'm not JSON"),
							},
						},
					},
				},
			},
			want: errors.Wrap(errors.Wrap(errors.New("json: error calling MarshalJSON for type *runtime.RawExtension: invalid character 'I' looking for beginning of value"), errVarMap), errOptions),
		},
		"DestroyError": {
			reason: "We should return any error we encounter destroying our tofu configuration",
			fields: fields{
				tofu: &MockTofu{
					MockDestroy: func(_ context.Context, _ ...opentofu.Option) error { return errBoom },
				},
			},
			args: args{
				mg: &v1beta1.Workspace{},
			},
			want: errors.Wrap(errBoom, errDestroy),
		},
		"Success": {
			reason: "We should not return an error if we successfully destroy the tofu configuration",
			fields: fields{
				tofu: &MockTofu{
					MockDestroy: func(_ context.Context, _ ...opentofu.Option) error { return nil },
				},
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
			},
			args: args{
				mg: &v1beta1.Workspace{
					Spec: v1beta1.WorkspaceSpec{
						ForProvider: v1beta1.WorkspaceParameters{
							DestroyArgs: []string{"-refresh=false"},
							Vars:        []v1beta1.Var{{Key: "super", Value: "cool"}},
							VarFiles: []v1beta1.VarFile{
								{
									Source:                v1beta1.VarFileSourceConfigMapKey,
									ConfigMapKeyReference: &v1beta1.KeyReference{},
								},
								{
									Source:             v1beta1.VarFileSourceSecretKey,
									SecretKeyReference: &v1beta1.KeyReference{},
									Format:             &v1beta1.FileFormatJSON,
								},
							},
						},
					},
				},
			},
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{tofu: tc.fields.tofu, kube: tc.fields.kube, logger: logging.NewNopLogger()}
			err := e.Delete(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}
