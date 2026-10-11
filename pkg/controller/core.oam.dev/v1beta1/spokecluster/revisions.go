/*
Copyright 2026 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package spokecluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// errBlueprintUnresolved marks a revision failure whose cause is the referenced
// blueprint, one of its planes or a pinned revision not being readable, as opposed to
// a write that failed. reconcileDispatch maps it to the BlueprintUnresolved reason.
var errBlueprintUnresolved = errors.New("blueprint unresolved")

// hashLen is how many hex characters of the sha256 digest name a revision. Eight is
// what image tags and git short SHAs settle on: enough to tell concurrent edits apart
// within one namespace, short enough to leave room for the plane name in a DNS label.
const hashLen = 8

// dispatchBundle is what ensureRevisions pins for one pass: the blueprint revision and
// its plane revisions, in blueprint order.
type dispatchBundle struct {
	blueprint *v1beta1.ClusterBlueprintRevision
	planes    []*v1beta1.ClusterPlaneRevision
}

// hashSpec is the first hashLen hex characters of sha256 over v's JSON. encoding/json
// writes struct fields in declaration order and a RawExtension as its Raw bytes, so the
// same spec always hashes the same and any change to a component, its properties or its
// ordering yields a new hash.
func hashSpec(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Only unmarshalable values reach here (channels, functions); API specs never do.
		panic(fmt.Sprintf("hashSpec: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:hashLen]
}

// revisionName is "<base>-<hash>", with base truncated when needed so the hash is kept
// whole and the result stays a DNS-1123 label.
func revisionName(base, hash string) string {
	return dnsLabel(base, "-"+hash)
}

// ensureRevisions resolves spec.blueprintRef into an immutable bundle. Without a
// revision pin it snapshots the live ClusterBlueprint and its ClusterPlanes: each plane
// becomes a ClusterPlaneRevision named by its content hash, and the blueprint becomes a
// ClusterBlueprintRevision that names those plane revisions. An unchanged spec resolves
// to the revisions that already exist, so the hub creates nothing on a steady pass. With
// a pin it reads that ClusterBlueprintRevision and the plane revisions it names, and
// snapshots nothing: a pin is a request to ship exactly that content.
func (r *Reconciler) ensureRevisions(ctx context.Context, sc *v1beta1.SpokeCluster) (*dispatchBundle, error) {
	ref := sc.Spec.BlueprintRef
	if ref == nil || ref.Name == "" {
		return nil, fmt.Errorf("%w: spec.blueprintRef.name is empty", errBlueprintUnresolved)
	}
	if ref.Revision != "" {
		return r.readPinnedRevisions(ctx, sc.Namespace, ref.Revision)
	}
	bp := &v1beta1.ClusterBlueprint{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: ref.Name}, bp); err != nil {
		return nil, fmt.Errorf("%w: reading ClusterBlueprint %s/%s: %v", errBlueprintUnresolved, sc.Namespace, ref.Name, err)
	}
	bundle := &dispatchBundle{}
	planes := make([]v1beta1.BlueprintPlaneRevision, 0, len(bp.Spec.Planes))
	for _, p := range bp.Spec.Planes {
		rev, err := r.ensurePlaneRevision(ctx, sc.Namespace, p.Ref.Name)
		if err != nil {
			return nil, err
		}
		bundle.planes = append(bundle.planes, rev)
		planes = append(planes, v1beta1.BlueprintPlaneRevision{
			Name:          p.Name,
			Phase:         phaseOrDefault(p.Phase),
			PlaneRevision: rev.Name,
			DependsOn:     p.DependsOn,
		})
	}
	bpRev, err := r.ensureBlueprintRevision(ctx, bp, planes)
	if err != nil {
		return nil, err
	}
	bundle.blueprint = bpRev
	return bundle, nil
}

// phaseOrDefault is the lifecycle phase a blueprint plane runs in. A plane that names
// none is an infrastructure plane, which is what the schema documents as the default.
func phaseOrDefault(phase v1beta1.LifecyclePhase) v1beta1.LifecyclePhase {
	if phase == "" {
		return v1beta1.LifecyclePhasePlaneProvisioning
	}
	return phase
}

// ensurePlaneRevision snapshots one ClusterPlane. The revision carries the plane's spec
// verbatim plus its own name and hash, is labelled for lookup by plane and by hash, and
// is owned by the plane so that deleting the plane collects its history.
func (r *Reconciler) ensurePlaneRevision(ctx context.Context, ns, planeName string) (*v1beta1.ClusterPlaneRevision, error) {
	plane := &v1beta1.ClusterPlane{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: planeName}, plane); err != nil {
		return nil, fmt.Errorf("%w: reading ClusterPlane %s/%s: %v", errBlueprintUnresolved, ns, planeName, err)
	}
	hash := hashSpec(plane.Spec)
	rev := &v1beta1.ClusterPlaneRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      revisionName(plane.Name, hash),
			Namespace: ns,
			Labels:    map[string]string{v1beta1.LabelPlaneName: plane.Name, v1beta1.LabelPlaneHash: hash},
		},
		Spec: v1beta1.ClusterPlaneRevisionSpec{
			PlaneName:   plane.Name,
			Hash:        hash,
			Description: plane.Spec.Description,
			Components:  plane.Spec.Components,
		},
	}
	if err := controllerutil.SetOwnerReference(plane, rev, r.Scheme); err != nil {
		return nil, fmt.Errorf("owning ClusterPlaneRevision %s/%s: %w", ns, rev.Name, err)
	}
	return rev, r.createRevisionIfAbsent(ctx, rev)
}

// ensureBlueprintRevision snapshots the blueprint's composition. The hash covers the
// plane entries, whose planeRevision names already embed each plane's content hash, so a
// change inside any plane produces a new blueprint revision too.
func (r *Reconciler) ensureBlueprintRevision(ctx context.Context, bp *v1beta1.ClusterBlueprint, planes []v1beta1.BlueprintPlaneRevision) (*v1beta1.ClusterBlueprintRevision, error) {
	spec := v1beta1.ClusterBlueprintRevisionSpec{
		BlueprintName: bp.Name,
		Description:   bp.Spec.Description,
		Planes:        planes,
	}
	spec.Hash = hashSpec(spec)
	rev := &v1beta1.ClusterBlueprintRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      revisionName(bp.Name, spec.Hash),
			Namespace: bp.Namespace,
			Labels:    map[string]string{v1beta1.LabelBlueprintName: bp.Name, v1beta1.LabelBlueprintHash: spec.Hash},
		},
		Spec: spec,
	}
	if err := controllerutil.SetOwnerReference(bp, rev, r.Scheme); err != nil {
		return nil, fmt.Errorf("owning ClusterBlueprintRevision %s/%s: %w", bp.Namespace, rev.Name, err)
	}
	return rev, r.createRevisionIfAbsent(ctx, rev)
}

// createRevisionIfAbsent makes a revision exist exactly once. An existing object is read
// back in place of the candidate and never rewritten: the name is the content hash, so
// whatever is stored under it is by construction the same content, and immutability is
// what lets a pin mean something. A concurrent create racing to the same name is the
// same situation and is read back the same way.
func (r *Reconciler) createRevisionIfAbsent(ctx context.Context, rev client.Object) error {
	key := client.ObjectKeyFromObject(rev)
	switch err := r.Get(ctx, key, rev); {
	case err == nil:
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("reading revision %s: %w", key, err)
	}
	switch err := r.Create(ctx, rev); {
	case err == nil:
		klog.InfoS("Revision created", "revision", key, "type", fmt.Sprintf("%T", rev))
		return nil
	case apierrors.IsAlreadyExists(err):
		return r.Get(ctx, key, rev)
	default:
		return fmt.Errorf("creating revision %s: %w", key, err)
	}
}

// readPinnedRevisions resolves a spec.blueprintRef.revision pin: the named
// ClusterBlueprintRevision and, in its order, the plane revisions it names.
func (r *Reconciler) readPinnedRevisions(ctx context.Context, ns, revision string) (*dispatchBundle, error) {
	bpRev := &v1beta1.ClusterBlueprintRevision{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: revision}, bpRev); err != nil {
		return nil, fmt.Errorf("%w: reading pinned ClusterBlueprintRevision %s/%s: %v", errBlueprintUnresolved, ns, revision, err)
	}
	bundle := &dispatchBundle{blueprint: bpRev}
	for _, p := range bpRev.Spec.Planes {
		rev := &v1beta1.ClusterPlaneRevision{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: p.PlaneRevision}, rev); err != nil {
			return nil, fmt.Errorf("%w: reading ClusterPlaneRevision %s/%s named by %s: %v", errBlueprintUnresolved, ns, p.PlaneRevision, revision, err)
		}
		bundle.planes = append(bundle.planes, rev)
	}
	return bundle, nil
}
