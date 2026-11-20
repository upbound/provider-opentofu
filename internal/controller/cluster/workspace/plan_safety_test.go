/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package workspace

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/upbound/provider-opentofu/apis/cluster/v1beta1"
	"github.com/upbound/provider-opentofu/internal/opentofu"
)

// TestPlanHistoryAbsentIsNoop guards against regressions: without
// writePlanToSecretRef, tofu plan must be invoked exactly as before, once, and
// neither the Kubernetes API nor 'tofu show' may be touched by Observe/Update.
func TestPlanHistoryAbsentIsNoop(t *testing.T) {
	cr := &v1beta1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws", UID: "uid"},
		Spec: v1beta1.WorkspaceSpec{ForProvider: v1beta1.WorkspaceParameters{
			PlanArgs: []string{"-refresh=false", "-parallelism=2"},
		}},
	}
	var diffArgs [][]string
	applies := 0
	tofu := &MockTofu{
		MockDiff: func(_ context.Context, o ...opentofu.Option) (bool, error) {
			diffArgs = append(diffArgs, opentofu.ArgsToString(o))
			return true, nil
		},
		MockShowPlan: func(_ context.Context, _ string) ([]byte, []byte, error) {
			t.Fatal("ShowPlan must not be called when plan history is disabled")
			return nil, nil, nil
		},
		MockApply:            func(_ context.Context, _ ...opentofu.Option) error { applies++; return nil },
		MockOutputs:          func(_ context.Context) ([]opentofu.Output, error) { return nil, nil },
		MockGenerateChecksum: func(_ context.Context) (string, error) { return "", nil },
	}
	fail := func(op string) error {
		t.Fatalf("the Kubernetes API (%s) must not be called when plan history is disabled", op)
		return nil
	}
	kube := &test.MockClient{
		MockGet:    func(context.Context, client.ObjectKey, client.Object) error { return fail("get") },
		MockList:   func(context.Context, client.ObjectList, ...client.ListOption) error { return fail("list") },
		MockCreate: func(context.Context, client.Object, ...client.CreateOption) error { return fail("create") },
		MockPatch:  func(context.Context, client.Object, client.Patch, ...client.PatchOption) error { return fail("patch") },
		MockDelete: func(context.Context, client.Object, ...client.DeleteOption) error { return fail("delete") },
		MockUpdate: func(context.Context, client.Object, ...client.UpdateOption) error { return fail("update") },
	}
	e := &external{tofu: tofu, kube: kube}

	differs, err := e.checkDiff(context.Background(), cr)
	if err != nil || !differs {
		t.Fatalf("checkDiff: want (true, nil), got (%v, %v)", differs, err)
	}
	if diff := cmp.Diff([][]string{{"-refresh=false", "-parallelism=2"}}, diffArgs); diff != "" {
		t.Errorf("tofu plan invocations: -want, +got:\n%s", diff)
	}
	if _, err := e.Update(context.Background(), cr); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if applies != 1 || e.planSecret != "" {
		t.Errorf("Update: want 1 apply and no plan secret, got %d applies and %q", applies, e.planSecret)
	}
}

// TestPlanHistoryIsBestEffort checks that plan history can never make a
// Workspace fail that would succeed without it.
func TestPlanHistoryIsBestEffort(t *testing.T) {
	errBoom := errors.New("boom")
	type want struct {
		differs  bool
		err      error
		withOut  []bool // one entry per tofu plan run: whether -out was passed
		showPlan bool
		history  int
		args     []string // args of the last plan run, when set
	}
	cases := map[string]struct {
		reason    string
		planArgs  []string
		diff      func(withOut bool) (bool, error)
		createErr error
		want      want
	}{
		"KubeFailure": {
			reason:    "Failing to write the history Secret must not fail the observation.",
			diff:      func(bool) (bool, error) { return true, nil },
			createErr: errBoom,
			want:      want{differs: true, withOut: []bool{true}, showPlan: true},
		},
		"SavedPlanUnsupported": {
			reason: "If tofu plan fails only when saving the plan, fall back to a plain plan.",
			diff: func(withOut bool) (bool, error) {
				if withOut {
					return false, errBoom
				}
				return true, nil
			},
			want: want{differs: true, withOut: []bool{true, false}},
		},
		"GenuinePlanFailure": {
			reason: "A plan that fails anyway must be reported exactly as without plan history.",
			diff:   func(bool) (bool, error) { return false, errBoom },
			want:   want{err: errors.Wrap(errBoom, errDiff), withOut: []bool{true, false}},
		},
		"UserPlanOut": {
			reason:   "A -out supplied in planArgs must be respected and not overridden.",
			planArgs: []string{"-out=/mine.tfplan"},
			diff:     func(bool) (bool, error) { return true, nil },
			want:     want{differs: true, withOut: []bool{true}, args: []string{"-out=/mine.tfplan"}},
		},
		"Recorded": {
			reason: "The nominal case records one revision with a single plan run.",
			diff:   func(bool) (bool, error) { return true, nil },
			want:   want{differs: true, withOut: []bool{true}, showPlan: true, history: 1},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := runtime.NewScheme()
			_ = corev1.AddToScheme(s)
			fk := fake.NewClientBuilder().WithScheme(s).Build()
			kube := &test.MockClient{
				MockList: fk.List,
				MockCreate: func(ctx context.Context, obj client.Object, o ...client.CreateOption) error {
					if tc.createErr != nil {
						return tc.createErr
					}
					return fk.Create(ctx, obj, o...)
				},
				MockDelete: fk.Delete,
				MockPatch:  fk.Patch,
			}
			cr := planTestWorkspace(nil)
			cr.Spec.ForProvider.PlanArgs = tc.planArgs
			var withOut []bool
			var lastArgs []string
			showPlan := false
			e := &external{kube: kube, tofu: &MockTofu{
				MockDiff: func(_ context.Context, o ...opentofu.Option) (bool, error) {
					lastArgs = opentofu.ArgsToString(o)
					hasOut := false
					for _, a := range lastArgs {
						hasOut = hasOut || strings.HasPrefix(a, "-out=")
					}
					withOut = append(withOut, hasOut)
					return tc.diff(hasOut)
				},
				MockShowPlan: func(context.Context, string) ([]byte, []byte, error) {
					showPlan = true
					return []byte("Plan: 1 to add"), []byte("{}"), nil
				},
			}}
			differs, err := e.checkDiff(context.Background(), cr)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ncheckDiff error: -want, +got:\n%s", tc.reason, diff)
			}
			if differs != tc.want.differs {
				t.Errorf("\n%s\ncheckDiff differs: want %v, got %v", tc.reason, tc.want.differs, differs)
			}
			if diff := cmp.Diff(tc.want.withOut, withOut); diff != "" {
				t.Errorf("\n%s\nplan runs (true = with -out): -want, +got:\n%s", tc.reason, diff)
			}
			if showPlan != tc.want.showPlan {
				t.Errorf("\n%s\nShowPlan called: want %v, got %v", tc.reason, tc.want.showPlan, showPlan)
			}
			if tc.want.args != nil {
				if diff := cmp.Diff(tc.want.args, lastArgs); diff != "" {
					t.Errorf("\n%s\nplan args: -want, +got:\n%s", tc.reason, diff)
				}
			}
			if got := history(t, fk); len(got) != tc.want.history {
				t.Errorf("\n%s\nhistory: want %d revisions, got %v", tc.reason, tc.want.history, got)
			}
		})
	}
}

// TestPlanHistoryMarkFailuresAreTolerated checks that failing to record the
// outcome of an apply never changes the outcome of Update.
func TestPlanHistoryMarkFailuresAreTolerated(t *testing.T) {
	errBoom := errors.New("boom")
	for name, applyErr := range map[string]error{"ApplySucceeded": nil, "ApplyFailed": errBoom} {
		t.Run(name, func(t *testing.T) {
			e := &external{
				planSecret: "plan.v1",
				kube: &test.MockClient{
					MockPatch: func(context.Context, client.Object, client.Patch, ...client.PatchOption) error { return errBoom },
				},
				tofu: &MockTofu{
					MockApply:            func(context.Context, ...opentofu.Option) error { return applyErr },
					MockOutputs:          func(context.Context) ([]opentofu.Output, error) { return nil, nil },
					MockGenerateChecksum: func(context.Context) (string, error) { return "", nil },
				},
			}
			_, err := e.Update(context.Background(), planTestWorkspace(nil))
			var want error
			if applyErr != nil {
				want = errors.Wrap(applyErr, errApply)
			}
			if diff := cmp.Diff(want, err, test.EquateErrors()); diff != "" {
				t.Errorf("Update error: -want, +got:\n%s", diff)
			}
		})
	}
}

func TestPlanSecretDataUTF8(t *testing.T) {
	// The text exceeds the limit and a 2-byte rune straddles the cut point, so
	// a naive byte cut would split it.
	text := []byte(strings.Repeat("x", maxPlanSecretSize-len(planTruncated)-1) + strings.Repeat("é", 200))
	data, _ := planSecretData(text, nil)
	if !strings.HasSuffix(string(data[PlanTextKey]), planTruncated) {
		t.Fatal("the text plan should have been truncated")
	}
	if !utf8.Valid(data[PlanTextKey]) {
		t.Error("truncated plan must remain valid UTF-8")
	}
	if len(data[PlanTextKey]) > maxPlanSecretSize {
		t.Errorf("truncated plan must fit, got %d bytes", len(data[PlanTextKey]))
	}
}
