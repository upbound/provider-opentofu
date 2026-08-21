//go:build invoke_tofu
// +build invoke_tofu

/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

// These tests invoke the opentofu binary. They require network access in
// order to download providers, and will thus not be run by default.
package opentofu

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
)

// tofu binary to invoke.
var tofuBinaryPath = func() string {
	if bin, ok := os.LookupEnv("TOFU_BINARY"); ok {
		return bin
	}
	return "tofu"
}()

// tofu test data. We need a fully qualified path because paths are
// relative to the tofu binary's working directory, not this test file.
var tofuTestDataPath = func() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "testdata")
}

func TestValidate(t *testing.T) {
	cases := map[string]struct {
		reason string
		module string
		ctx    context.Context
		want   error
	}{
		"ValidModule": {
			reason: "We should not return an error if the module is valid.",
			module: "testdata/validmodule",
			ctx:    context.Background(),
			want:   nil,
		},
		"InvalidModule": {
			reason: "We should return an error if the module is invalid.",
			module: "testdata/invalidmodule",
			ctx:    context.Background(),
			want:   errors.Errorf(errFmtInvalidConfig, 1),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Validation is a read-only operation, so we operate directly on
			// our test data instead of creating a temporary directory.
			tofu := Harness{Path: tofuBinaryPath, Dir: tc.module}
			got := tofu.Validate(tc.ctx)

			if diff := cmp.Diff(tc.want, got, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ntofu.Validate(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestWorkspace(t *testing.T) {
	type args struct {
		ctx  context.Context
		name string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   error
	}{
		"SuccessfulSelect": {
			reason: "It should be possible to select the default workspace, which always exists.",
			args: args{
				ctx:  context.Background(),
				name: "default",
			},
			want: nil,
		},
		"SuccessfulNew": {
			reason: "It should be possible to create a new workspace.",
			args: args{
				ctx:  context.Background(),
				name: "cool",
			},
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "provider-opentofu-test")
			if err != nil {
				t.Fatalf("Cannot create temporary directory: %v", err)
			}
			defer os.RemoveAll(dir)

			tf := Harness{Path: tofuBinaryPath, Dir: dir}
			got := tf.Workspace(tc.args.ctx, tc.args.name)

			if diff := cmp.Diff(tc.want, got, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ntf.Workspace(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestDeleteWorkspace(t *testing.T) {
	type args struct {
		ctx  context.Context
		name string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   error
	}{
		"SuccessfulDelete": {
			reason: "It should be possible to delete an existing workspace.",
			args: args{
				ctx:  context.Background(),
				name: "cool",
			},
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "provider-opentofu-test")
			if err != nil {
				t.Fatalf("Cannot create temporary directory: %v", err)
			}
			defer os.RemoveAll(dir)

			tf := Harness{Path: tofuBinaryPath, Dir: dir}
			_ = tf.Workspace(tc.args.ctx, tc.args.name)
			got := tf.DeleteCurrentWorkspace(tc.args.ctx)

			if diff := cmp.Diff(tc.want, got, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ntf.Workspace(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestOutputs(t *testing.T) {
	type want struct {
		outputs []Output
		err     error
	}
	cases := map[string]struct {
		reason string
		module string
		ctx    context.Context
		want   want
	}{
		"ManyOutputs": {
			reason: "We should return outputs from a module.",
			module: "testdata/outputmodule",
			ctx:    context.Background(),
			want: want{
				outputs: []Output{
					{Name: "bool", Type: OutputTypeBool, value: true},
					{Name: "number", Type: OutputTypeNumber, value: float64(42)},
					{
						Name:  "object",
						Type:  OutputTypeObject,
						value: map[string]any{"wow": "suchobject"},
					},
					{Name: "sensitive", Sensitive: true, Type: OutputTypeString, value: "very"},
					{Name: "string", Type: OutputTypeString, value: "very"},
					{
						Name:  "tuple",
						Type:  OutputTypeTuple,
						value: []any{"a", "really", "long", "tuple"},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Reading output is a read-only operation, so we operate directly
			// on our test data instead of creating a temporary directory.
			tf := Harness{Path: tofuBinaryPath, Dir: tc.module}
			got, err := tf.Outputs(tc.ctx)

			if diff := cmp.Diff(tc.want.outputs, got, cmp.AllowUnexported(Output{})); diff != "" {
				t.Errorf("\n%s\ntf.Outputs(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ntf.Outputs(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestResources(t *testing.T) {
	type want struct {
		resources []string
		err       error
	}
	cases := map[string]struct {
		reason string
		module string
		ctx    context.Context
		want   want
	}{
		"ModuleWithResources": {
			reason: "We should return resources from a module.",
			module: "testdata/nullmodule",
			ctx:    context.Background(),
			want: want{
				resources: []string{"null_resource.test", "random_id.test"},
			},
		},
		"ModuleWithoutResources": {
			reason: "We should not return resources from a module when there are none.",
			module: "testdata/outputmodule",
			ctx:    context.Background(),
			want: want{
				resources: []string{},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Reading output is a read-only operation, so we operate directly
			// on our test data instead of creating a temporary directory.
			tf := Harness{Path: tofuBinaryPath, Dir: tc.module}
			got, err := tf.Resources(tc.ctx)

			if diff := cmp.Diff(tc.want.resources, got, cmp.AllowUnexported(Output{})); diff != "" {
				t.Errorf("\n%s\ntf.Resources(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ntf.Resources(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestInitDiffApplyDestroy(t *testing.T) {
	type initArgs struct {
		ctx context.Context
		o   []InitOption
	}
	type args struct {
		ctx context.Context
		o   []Option
	}
	type want struct {
		init    error
		diff    error
		apply   error
		destroy error

		differsBeforeApply bool
		differsAfterApply  bool
	}

	cases := map[string]struct {
		reason      string
		initArgs    initArgs
		diffArgs    args
		applyArgs   args
		destroyArgs args
		want        want
	}{
		"Simple": {
			reason: "It should be possible to initialize, apply, and destroy a simple Terraform module",
			initArgs: initArgs{
				ctx: context.Background(),
				o:   []InitOption{FromModule(filepath.Join(tofuTestDataPath(), "nullmodule"))},
			},
			applyArgs: args{
				ctx: context.Background(),
			},
			diffArgs: args{
				ctx: context.Background(),
			},
			destroyArgs: args{
				ctx: context.Background(),
			},
			want: want{
				differsBeforeApply: false,
			},
		},
		"WithVar": {
			reason: "It should be possible to initialize a simple Terraform module, then apply and destroy it with a supplied variable",
			initArgs: initArgs{
				ctx: context.Background(),
				o:   []InitOption{FromModule(filepath.Join(tofuTestDataPath(), "nullmodule"))},
			},
			applyArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVar("coolness", "extreme")},
			},
			diffArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVar("coolness", "extreme")},
			},
			destroyArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVar("coolness", "extreme")},
			},
			want: want{
				differsBeforeApply: true,
			},
		},
		"WithHCLVarFile": {
			reason: "It should be possible to initialize a simple Terraform module, then apply and destroy it with a supplied HCL file of variables",
			initArgs: initArgs{
				ctx: context.Background(),
				o:   []InitOption{FromModule(filepath.Join(tofuTestDataPath(), "nullmodule"))},
			},
			diffArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVarFile([]byte(`coolness = "extreme!"`), HCL)},
			},
			applyArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVarFile([]byte(`coolness = "extreme!"`), HCL)},
			},
			destroyArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVarFile([]byte(`coolness = "extreme!"`), HCL)},
			},
			want: want{
				differsBeforeApply: true,
			},
		},
		"WithJSONVarFile": {
			reason: "It should be possible to initialize a simple Terraform module, then apply and destroy it with a supplied JSON file of variables",
			initArgs: initArgs{
				ctx: context.Background(),
				o:   []InitOption{FromModule(filepath.Join(tofuTestDataPath(), "nullmodule"))},
			},
			diffArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVarFile([]byte(`{"coolness":"extreme!"}`), JSON)},
			},
			applyArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVarFile([]byte(`{"coolness":"extreme!"}`), JSON)},
			},
			destroyArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVarFile([]byte(`{"coolness":"extreme!"}`), JSON)},
			},
			want: want{
				differsBeforeApply: true,
			},
		},
		// NOTE(negz): The goal of these error case tests is to validate that
		// any kind of error classification is happening. We don't want to test
		// too many error cases, because doing so would likely create an overly
		// tight coupling to a particular version of the opentofu binary.
		"ModuleNotFound": {
			reason: "Init should return an error when asked to initialize from a module that doesn't exist",
			initArgs: initArgs{
				ctx: context.Background(),
				o:   []InitOption{FromModule("./nonexistent")},
			},
			diffArgs: args{
				ctx: context.Background(),
			},
			applyArgs: args{
				ctx: context.Background(),
			},
			destroyArgs: args{
				ctx: context.Background(),
			},
			want: want{
				init:  errors.New("failed to download module"),
				diff:  errors.New("no configuration files"),
				apply: errors.New("no configuration files"),
				// Apparently destroy 'works' in this situation ¯\_(ツ)_/¯
			},
		},
		"UndeclaredVar": {
			reason: "Destroy should return an error when supplied a variable not declared by the module",
			initArgs: initArgs{
				ctx: context.Background(),
				o:   []InitOption{FromModule(filepath.Join(tofuTestDataPath(), "nullmodule"))},
			},
			diffArgs: args{
				ctx: context.Background(),
			},
			applyArgs: args{
				ctx: context.Background(),
			},
			destroyArgs: args{
				ctx: context.Background(),
				o:   []Option{WithVar("boop", "doop!")},
			},
			want: want{
				destroy: errors.New("value for undeclared variable"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// t.Parallel()

			dir, err := os.MkdirTemp("", "provider-opentofu-test")
			if err != nil {
				t.Fatalf("Cannot create temporary directory: %v", err)
			}
			defer os.RemoveAll(dir)

			tf := Harness{Path: tofuBinaryPath, Dir: dir, UsePluginCache: false}

			err = tf.Init(tc.initArgs.ctx, tc.initArgs.o...)
			checkErr(t, tc.reason, "tf.Init(...)", tc.want.init, err)

			differs, err := tf.Diff(tc.diffArgs.ctx, tc.diffArgs.o...)
			t.Logf("Want %t, got %t", tc.want.differsBeforeApply, differs)
			checkErr(t, tc.reason, "tf.Diff(...) (before apply)", tc.want.diff, err)
			if diff := cmp.Diff(tc.want.differsBeforeApply, differs); diff != "" {
				t.Errorf("\n%s\ntf.Diff(...): -want, +got (before apply):\n%s", tc.reason, diff)
			}

			err = tf.Apply(tc.applyArgs.ctx, tc.applyArgs.o...)
			checkErr(t, tc.reason, "tf.Apply(...)", tc.want.apply, err)

			differs, err = tf.Diff(tc.diffArgs.ctx, tc.diffArgs.o...)
			checkErr(t, tc.reason, "tf.Diff(...) (after apply)", tc.want.diff, err)
			if diff := cmp.Diff(tc.want.differsAfterApply, differs); diff != "" {
				t.Errorf("\n%s\ntf.Diff(...): -want, +got (after apply):\n%s", tc.reason, diff)
			}

			err = tf.Destroy(tc.destroyArgs.ctx, tc.destroyArgs.o...)
			checkErr(t, tc.reason, "tf.Destroy(...)", tc.want.destroy, err)
		})
	}
}

// checkErr compares a wanted error against one the harness returned. Errors
// originating from the tofu binary are wrapped by Classify, which replaces the
// raw output with a summary line plus a compressed copy of the full output, so
// an exact comparison would couple these tests to a specific tofu version's
// wording. The wanted error is therefore matched as a case-insensitive
// substring: enough to prove the error was classified, loose enough to survive
// a version bump.
func checkErr(t *testing.T, reason, op string, want, got error) {
	t.Helper()

	switch {
	case want == nil && got != nil:
		t.Errorf("\n%s\n%s: want no error, got: %v", reason, op, got)
	case want != nil && got == nil:
		t.Errorf("\n%s\n%s: want an error containing %q, got none", reason, op, want.Error())
	case want != nil && !strings.Contains(strings.ToLower(got.Error()), strings.ToLower(want.Error())):
		t.Errorf("\n%s\n%s: want an error containing %q, got: %v", reason, op, want.Error(), got)
	}
}

var md5Sum = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TestGenerateChecksum checks the properties the caller actually relies on -
// that the checksum is well formed, stable for unchanged content, and changes
// when the workspace does - rather than a hard coded digest. A literal digest
// would have to be recomputed whenever the test data changes, and says nothing
// about whether change is detected, which is the only reason the checksum
// exists: it is what decides whether `tofu init` needs to run again.
func TestGenerateChecksum(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("# empty\n"), 0o600); err != nil {
		t.Fatalf("Cannot write test module: %v", err)
	}

	tf := Harness{Path: tofuBinaryPath, Dir: dir}

	first, err := tf.GenerateChecksum(ctx)
	if err != nil {
		t.Fatalf("tf.GenerateChecksum(...): unexpected error: %v", err)
	}
	if !md5Sum.MatchString(first) {
		t.Fatalf("tf.GenerateChecksum(...): want an md5 sum, got %q", first)
	}

	again, err := tf.GenerateChecksum(ctx)
	if err != nil {
		t.Fatalf("tf.GenerateChecksum(...): unexpected error: %v", err)
	}
	if again != first {
		t.Errorf("The checksum should be stable while the workspace is unchanged: got %q then %q", first, again)
	}

	if err := os.WriteFile(filepath.Join(dir, "variables.tf"), []byte("variable \"coolness\" {}\n"), 0o600); err != nil {
		t.Fatalf("Cannot write test module: %v", err)
	}

	changed, err := tf.GenerateChecksum(ctx)
	if err != nil {
		t.Fatalf("tf.GenerateChecksum(...): unexpected error: %v", err)
	}
	if changed == first {
		t.Errorf("The checksum should change when the workspace does: got %q both before and after adding a file", first)
	}
}
