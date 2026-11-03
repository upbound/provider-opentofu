/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package workspace

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/upbound/provider-opentofu/apis/cluster/v1beta1"
	"github.com/upbound/provider-opentofu/internal/opentofu"
)

const testPlanNS = "ns"

func planTestWorkspace(limit *int32) *v1beta1.Workspace {
	return &v1beta1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws", UID: "uid"},
		Spec: v1beta1.WorkspaceSpec{ForProvider: v1beta1.WorkspaceParameters{
			WritePlanToSecretRef: &v1beta1.PlanHistoryReference{Name: "plan", Namespace: testPlanNS, HistoryLimit: limit},
		}},
	}
}

// planHarness simulates tofu: the "current plan" is whatever the test sets.
type planHarness struct {
	t        *testing.T
	text     string
	differs  bool
	applyErr error
	planArgs []string
}

func (h *planHarness) tofu() *MockTofu {
	return &MockTofu{
		MockDiff: func(_ context.Context, o ...opentofu.Option) (bool, error) {
			h.planArgs = opentofu.ArgsToString(o)
			return h.differs, nil
		},
		MockShowPlan: func(_ context.Context, f string) ([]byte, []byte, error) {
			if _, err := os.Stat(f); err != nil {
				h.t.Errorf("plan file %q should exist while rendering: %v", f, err)
			}
			return []byte(h.text), []byte(`{"plan":"` + h.text + `"}`), nil
		},
		MockApply:            func(_ context.Context, _ ...opentofu.Option) error { return h.applyErr },
		MockOutputs:          func(_ context.Context) ([]opentofu.Output, error) { return nil, nil },
		MockGenerateChecksum: func(_ context.Context) (string, error) { return "", nil },
	}
}

// history returns "name=status" for each revision, oldest first.
func history(t *testing.T, kube client.Client) []string {
	t.Helper()
	e := &external{kube: kube}
	revs, err := e.planRevisions(context.Background(), testPlanNS, "uid")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(revs))
	for _, s := range revs {
		out = append(out, s.GetName()+"="+s.GetLabels()[LabelPlanStatus])
	}
	return out
}

func TestPlanHistoryLifecycle(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	kube := fake.NewClientBuilder().WithScheme(s).Build()
	now = func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()

	h := &planHarness{t: t}
	cr := planTestWorkspace(ptr.To[int32](3))
	ctx := context.Background()

	// reconcile simulates one reconcile: Observe, then Update if not up to date.
	reconcile := func(step string) {
		t.Helper()
		e := &external{tofu: h.tofu(), kube: kube}
		differs, err := e.checkDiff(ctx, cr)
		if err != nil {
			t.Fatalf("%s: checkDiff: %v", step, err)
		}
		if differs && cr.GetManagementPolicies() == nil {
			_, _ = e.Update(ctx, cr)
		}
	}
	check := func(step string, want ...string) {
		t.Helper()
		if diff := cmp.Diff(want, history(t, kube)); diff != "" {
			t.Errorf("%s: -want history, +got history:\n%s", step, diff)
		}
	}

	// Observe-only: the plan is recorded as pending and stays pending.
	cr.SetManagementPolicies(xpv2.ManagementPolicies{xpv2.ManagementActionObserve})
	h.text, h.differs = "Plan: 1 to add", true
	reconcile("first plan")
	check("first plan", "plan.v1=pending")

	// Polling with an unchanged plan does not add revisions.
	reconcile("same plan")
	check("same plan", "plan.v1=pending")

	// A different plan supersedes the pending one.
	h.text = "Plan: 2 to add"
	reconcile("new plan")
	check("new plan", "plan.v1=superseded", "plan.v2=pending")

	// Applying marks the plan observed in the same reconcile as applied.
	cr.SetManagementPolicies(nil)
	reconcile("apply")
	check("apply", "plan.v1=superseded", "plan.v2=applied")

	// Converging records a no-op revision, once.
	h.text, h.differs = "No changes.", false
	reconcile("converged")
	check("converged", "plan.v1=superseded", "plan.v2=applied", "plan.v3=no-op")
	reconcile("converged again")
	check("converged again", "plan.v1=superseded", "plan.v2=applied", "plan.v3=no-op")

	// A failed apply is recorded as such; the history limit of 3 prunes v1.
	h.text, h.differs, h.applyErr = "Plan: 1 to change", true, errors.New("boom")
	reconcile("failed apply")
	check("failed apply", "plan.v2=applied", "plan.v3=no-op", "plan.v4=failed")

	// Retrying the same plan successfully flips it to applied without a new revision.
	h.applyErr = nil
	reconcile("retry")
	check("retry", "plan.v2=applied", "plan.v3=no-op", "plan.v4=applied")

	// Inspect one revision in detail.
	got := &corev1.Secret{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: testPlanNS, Name: "plan.v4"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Type != PlanSecretType {
		t.Errorf("type: want %q, got %q", PlanSecretType, got.Type)
	}
	if string(got.Data[PlanTextKey]) != "Plan: 1 to change" || len(got.Data[PlanJSONKey]) == 0 {
		t.Errorf("unexpected data: %v", got.Data)
	}
	wantLabels := map[string]string{LabelWorkspace: "ws", LabelWorkspaceUID: "uid", LabelPlanRevision: "4", LabelPlanStatus: PlanStatusApplied}
	if diff := cmp.Diff(wantLabels, got.Labels); diff != "" {
		t.Errorf("labels: -want, +got:\n%s", diff)
	}
	for _, a := range []string{AnnotationPlanHash, AnnotationPlannedAt, AnnotationAppliedAt, AnnotationFailedAt} {
		if got.Annotations[a] == "" {
			t.Errorf("missing annotation %s", a)
		}
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != "uid" || got.OwnerReferences[0].Kind != v1beta1.WorkspaceKind {
		t.Errorf("unexpected owner references: %v", got.OwnerReferences)
	}

	// The temporary plan file must be passed to tofu and cleaned up.
	var planFile string
	for _, a := range h.planArgs {
		if strings.HasPrefix(a, "-out=") {
			planFile = strings.TrimPrefix(a, "-out=")
		}
	}
	if planFile == "" {
		t.Fatalf("plan should be saved with -out, got args %v", h.planArgs)
	}
	if _, err := os.Stat(planFile); !os.IsNotExist(err) {
		t.Errorf("plan file %q should have been removed", planFile)
	}
}

func TestPlanHistoryDisabled(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	kube := fake.NewClientBuilder().WithScheme(s).Build()
	h := &planHarness{t: t, text: "Plan: 1 to add", differs: true}

	for name, cr := range map[string]*v1beta1.Workspace{
		"NoRef": {ObjectMeta: metav1.ObjectMeta{Name: "ws", UID: "uid"}},
		"Deleted": func() *v1beta1.Workspace {
			cr := planTestWorkspace(nil)
			n := metav1.Now()
			cr.SetDeletionTimestamp(&n)
			return cr
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			e := &external{tofu: h.tofu(), kube: kube}
			if _, err := e.checkDiff(context.Background(), cr); err != nil {
				t.Fatal(err)
			}
			for _, a := range h.planArgs {
				if strings.HasPrefix(a, "-out=") {
					t.Errorf("no plan file should be requested, got %v", h.planArgs)
				}
			}
			if got := history(t, kube); len(got) != 0 {
				t.Errorf("no history should be recorded, got %v", got)
			}
		})
	}
}

func TestPlanSecretData(t *testing.T) {
	small := []byte("plan")
	big := []byte(strings.Repeat("x", maxPlanSecretSize))
	huge := []byte(strings.Repeat("x", maxPlanSecretSize+10))

	data, omitted := planSecretData(small, small)
	if omitted || len(data) != 2 {
		t.Errorf("small plans should keep both keys, got %d keys (omitted=%v)", len(data), omitted)
	}

	data, omitted = planSecretData(big, small)
	if !omitted || data[PlanJSONKey] != nil || len(data[PlanTextKey]) != maxPlanSecretSize {
		t.Errorf("JSON should be dropped when both do not fit")
	}

	data, omitted = planSecretData(huge, small)
	if !omitted || len(data[PlanTextKey]) != maxPlanSecretSize || !strings.HasSuffix(string(data[PlanTextKey]), planTruncated) {
		t.Errorf("oversized text plan should be truncated to %d bytes, got %d", maxPlanSecretSize, len(data[PlanTextKey]))
	}
}
