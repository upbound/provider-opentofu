/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/upbound/provider-opentofu/apis/namespaced/v1beta1"
)

const (
	errPlanFile    = "cannot create temporary plan file"
	errShowPlan    = "cannot render tofu plan"
	errListPlans   = "cannot list plan history Secrets"
	errCreatePlan  = "cannot create plan history Secret"
	errPatchPlan   = "cannot update plan history Secret status"
	errPrunePlans  = "cannot prune plan history Secrets"
	errMarkApplied = "cannot mark plan as applied"

	// PlanTextKey is the Secret key holding the human-readable plan.
	PlanTextKey = "plan.txt"
	// PlanJSONKey is the Secret key holding the 'tofu show -json' plan.
	PlanJSONKey = "plan.json"

	// PlanSecretType is the type of plan history Secrets.
	PlanSecretType corev1.SecretType = "opentofu.upbound.io/plan.v1"

	// Labels set on every plan history Secret.
	LabelWorkspace    = "opentofu.upbound.io/workspace"
	LabelWorkspaceUID = "opentofu.upbound.io/workspace-uid"
	LabelPlanRevision = "opentofu.upbound.io/plan-revision"
	LabelPlanStatus   = "opentofu.upbound.io/plan-status"

	// Annotations set on plan history Secrets.
	AnnotationPlanHash        = "opentofu.upbound.io/plan-hash"
	AnnotationPlannedAt       = "opentofu.upbound.io/planned-at"
	AnnotationAppliedAt       = "opentofu.upbound.io/applied-at"
	AnnotationFailedAt        = "opentofu.upbound.io/failed-at"
	AnnotationPlanJSONOmitted = "opentofu.upbound.io/plan-json-omitted"

	// Kubernetes caps Secrets at 1MiB. Leave some headroom for metadata.
	maxPlanSecretSize   = 1000 * 1000
	planTruncated       = "\n\n[plan truncated: it exceeds the maximum Secret size]\n"
	defaultHistoryLimit = 10
)

// Plan statuses.
const (
	PlanStatusPending    = "pending"
	PlanStatusApplied    = "applied"
	PlanStatusFailed     = "failed"
	PlanStatusSuperseded = "superseded"
	PlanStatusNoOp       = "no-op"
)

// now is overridden in tests.
var now = time.Now

// newPlanFile returns the path of a temporary file to which tofu plan can
// write its plan. The file lives outside of the workspace directory so that
// it does not affect the workspace checksum. Callers must remove it.
func newPlanFile() (string, error) {
	f, err := os.CreateTemp("", "tofu-*.tfplan")
	if err != nil {
		return "", errors.Wrap(err, errPlanFile)
	}
	return f.Name(), errors.Wrap(f.Close(), errPlanFile)
}

func planNamespace(cr *v1beta1.Workspace) string {
	return cr.GetNamespace()
}

// recordPlan renders the supplied plan file and records it in the plan
// history, unless it is identical to the latest recorded revision. It
// remembers the latest revision so Update can mark it applied or failed.
func (c *external) recordPlan(ctx context.Context, cr *v1beta1.Workspace, planFile string, differs bool) error {
	ref := cr.Spec.ForProvider.WritePlanToSecretRef
	ns := planNamespace(cr)

	text, js, err := c.tofu.ShowPlan(ctx, planFile)
	if err != nil {
		return errors.Wrap(err, errShowPlan)
	}
	sum := sha256.Sum256(text)
	hash := hex.EncodeToString(sum[:])

	revs, err := c.planRevisions(ctx, ns, cr.GetUID())
	if err != nil {
		return err
	}

	rev := 1
	if n := len(revs); n > 0 {
		latest := &revs[n-1]
		if latest.GetAnnotations()[AnnotationPlanHash] == hash {
			// Nothing changed since the last plan was recorded.
			c.planSecret = latest.GetName()
			return nil
		}
		if latest.GetLabels()[LabelPlanStatus] == PlanStatusPending {
			if err := c.setPlanStatus(ctx, ns, latest.GetName(), PlanStatusSuperseded, ""); err != nil {
				return err
			}
		}
		rev = planRevision(latest) + 1
	}

	status := PlanStatusNoOp
	if differs {
		status = PlanStatusPending
	}
	data, omitted := planSecretData(text, js)
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.v%d", ref.Name, rev),
			Namespace: ns,
			Labels: map[string]string{
				LabelWorkspaceUID: string(cr.GetUID()),
				LabelPlanRevision: strconv.Itoa(rev),
				LabelPlanStatus:   status,
			},
			Annotations: map[string]string{
				AnnotationPlanHash:  hash,
				AnnotationPlannedAt: now().UTC().Format(time.RFC3339),
			},
		},
		Type: PlanSecretType,
		Data: data,
	}
	// Label values are limited to 63 characters; the UID label is the one
	// relied upon, the name label is a convenience.
	if len(validation.IsValidLabelValue(cr.GetName())) == 0 {
		s.Labels[LabelWorkspace] = cr.GetName()
	}
	if omitted {
		s.Annotations[AnnotationPlanJSONOmitted] = "true"
	}
	meta.AddOwnerReference(s, meta.AsOwner(meta.TypedReferenceTo(cr, v1beta1.WorkspaceGroupVersionKind)))
	if err := c.kube.Create(ctx, s); err != nil {
		return errors.Wrap(err, errCreatePlan)
	}
	c.planSecret = s.GetName()

	return c.prunePlans(ctx, append(revs, *s), historyLimit(ref))
}

// markPlan records the outcome of applying the latest plan observed during
// this reconcile, if any.
func (c *external) markPlan(ctx context.Context, cr *v1beta1.Workspace, status string) error {
	if cr.Spec.ForProvider.WritePlanToSecretRef == nil || c.planSecret == "" {
		return nil
	}
	ann := AnnotationAppliedAt
	if status == PlanStatusFailed {
		ann = AnnotationFailedAt
	}
	return c.setPlanStatus(ctx, planNamespace(cr), c.planSecret, status, ann)
}

// setPlanStatus patches the status label, and optionally a timestamp
// annotation, of a plan history Secret without reading it first.
func (c *external) setPlanStatus(ctx context.Context, ns, name, status, timestampAnnotation string) error {
	m := map[string]any{"labels": map[string]string{LabelPlanStatus: status}}
	if timestampAnnotation != "" {
		m["annotations"] = map[string]string{timestampAnnotation: now().UTC().Format(time.RFC3339)}
	}
	p, err := json.Marshal(map[string]any{"metadata": m})
	if err != nil {
		return errors.Wrap(err, errPatchPlan)
	}
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	return errors.Wrap(c.kube.Patch(ctx, s, client.RawPatch(types.MergePatchType, p)), errPatchPlan)
}

// planRevisions returns the plan history of a Workspace, oldest first.
func (c *external) planRevisions(ctx context.Context, ns string, uid types.UID) ([]corev1.Secret, error) {
	l := &corev1.SecretList{}
	if err := c.kube.List(ctx, l, client.InNamespace(ns), client.MatchingLabels{LabelWorkspaceUID: string(uid)}); err != nil {
		return nil, errors.Wrap(err, errListPlans)
	}
	sort.SliceStable(l.Items, func(i, j int) bool { return planRevision(&l.Items[i]) < planRevision(&l.Items[j]) })
	return l.Items, nil
}

// prunePlans deletes the oldest revisions beyond the history limit.
func (c *external) prunePlans(ctx context.Context, revs []corev1.Secret, limit int) error {
	for i := 0; i < len(revs)-limit; i++ {
		if err := c.kube.Delete(ctx, &revs[i]); client.IgnoreNotFound(err) != nil {
			return errors.Wrap(err, errPrunePlans)
		}
	}
	return nil
}

func planRevision(s *corev1.Secret) int {
	r, _ := strconv.Atoi(s.GetLabels()[LabelPlanRevision])
	return r
}

func historyLimit(ref *v1beta1.PlanHistoryReference) int {
	if ref.HistoryLimit == nil || *ref.HistoryLimit < 1 {
		return defaultHistoryLimit
	}
	return int(*ref.HistoryLimit)
}

// planSecretData builds the Secret payload, dropping the JSON plan and then
// truncating the text plan if they do not fit in a Secret. It reports whether
// the JSON plan was omitted.
func planSecretData(text, js []byte) (map[string][]byte, bool) {
	if len(text)+len(js) <= maxPlanSecretSize {
		return map[string][]byte{PlanTextKey: text, PlanJSONKey: js}, false
	}
	if len(text) > maxPlanSecretSize {
		text = append(text[:maxPlanSecretSize-len(planTruncated):maxPlanSecretSize-len(planTruncated)], planTruncated...)
	}
	return map[string][]byte{PlanTextKey: text}, true
}
