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
	"errors"
	"time"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
)

// spokeCluster is the Cluster CR as the spoke-side controller would leave it: two planes,
// one of them degraded unless healthy says otherwise.
func spokeCluster(sc *v1beta1.SpokeCluster, phase v1beta1.ClusterPhase, healthy bool) *v1beta1.Cluster {
	planesHealthy := 1
	status := "Degraded"
	if healthy {
		planesHealthy = 2
		status = "Healthy"
	}
	checked := metav1.NewTime(time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC))
	return &v1beta1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: sc.Name, Namespace: sc.Namespace},
		Status: v1beta1.ClusterStatus{
			Phase: phase,
			Planes: []v1beta1.PlaneStatus{
				{Name: "cni", Phase: v1beta1.LifecyclePhaseClusterInit, Application: "platform-cni", Healthy: true},
				{Name: "infra", Phase: v1beta1.LifecyclePhasePlaneProvisioning, Application: "platform-infra", Healthy: healthy, Message: "eks: creating"},
			},
			Health: &v1beta1.ClusterHealth{Status: status, PlanesHealthy: planesHealthy, PlanesTotal: 2, LastCheckTime: &checked},
		},
	}
}

// seedSpokeCluster puts the Cluster where only the spoke reader can see it.
func seedSpokeCluster(r *Reconciler, cluster *v1beta1.Cluster) {
	Expect(r.SpokeReader.Create(context.Background(), cluster)).To(Succeed())
}

// interceptedSpokeReader is a fresh spoke reader seeded with objs whose calls pass
// through funcs first. It replaces, rather than wraps, the reader newTestReconciler built,
// because the interceptor needs the fake's WithWatch shape.
func interceptedSpokeReader(t GinkgoTInterface, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	return interceptor.NewClient(fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build(), funcs)
}

var _ = It("PullSpokeStatusProjectsReadyCluster", func() {
	t := GinkgoT()
	sc := dispatchSpoke("cpspoke1")
	r := connectedReconciler(t, sc, append([]client.Object{dispatchBlueprint(sc.Namespace)}, dispatchPlanes(sc.Namespace)...)...)
	var seenCluster string
	r.SpokeReader = interceptedSpokeReader(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*v1beta1.Cluster); ok {
				seenCluster = multicluster.ClusterNameInContext(ctx)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}, spokeCluster(sc, v1beta1.ClusterPhaseReady, true))

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	Expect(seenCluster).To(Equal("cpspoke1"), "the Cluster is read on the spoke, through the gateway")

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionTrue, reasonSpokeReady)
	health := latest.Status.Health
	Expect(health).NotTo(BeNil())
	Expect(health.Status).To(Equal("Healthy"))
	Expect(health.Phase).To(Equal("Ready"))
	Expect(health.PlanesHealthy).To(Equal(2))
	Expect(health.PlanesTotal).To(Equal(2))
	Expect(health.LastPulledAt).NotTo(BeNil())
	Expect(health.LastPulledAt.Time).To(BeTemporally("~", time.Now(), time.Minute))
	Expect(health.Planes).To(Equal([]v1beta1.SpokePlaneHealth{
		{Name: "cni", Phase: "clusterInit", Healthy: true},
		{Name: "infra", Phase: "planeProvisioning", Healthy: true, Message: "eks: creating"},
	}))
})

var _ = It("PullSpokeStatusReportsNotReadyWithPhaseAndCounts", func() {
	t := GinkgoT()
	sc := dispatchSpoke("cpspoke1")
	r := connectedReconciler(t, sc, append([]client.Object{dispatchBlueprint(sc.Namespace)}, dispatchPlanes(sc.Namespace)...)...)
	seedSpokeCluster(r, spokeCluster(sc, v1beta1.ClusterPhaseProvisioning, false))

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionFalse, reasonSpokeNotReady)
	cond := meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionSpokeReady)
	Expect(cond.Message).To(ContainSubstring("Provisioning"))
	Expect(cond.Message).To(ContainSubstring("1/2"))
	Expect(latest.Status.Health.Status).To(Equal("Degraded"))
	Expect(latest.Status.Health.Phase).To(Equal("Provisioning"))
	Expect(latest.Status.Health.PlanesHealthy).To(Equal(1))
})

// A Cluster someone created on the spoke by hand is still reported: the pull does not
// depend on spec.blueprintRef.
var _ = It("PullSpokeStatusRunsWithoutBlueprintRef", func() {
	t := GinkgoT()
	sc := connectableSpoke("cpspoke1")
	r := connectedReconciler(t, sc)
	seedSpokeCluster(r, spokeCluster(sc, v1beta1.ClusterPhaseReady, true))

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionTrue, reasonSpokeReady)
	Expect(latest.Status.Health).NotTo(BeNil())
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionDispatched)).To(BeNil())
})

var _ = It("PullSpokeStatusClearsHealthWhenClusterIsAbsent", func() {
	t := GinkgoT()
	sc := connectableSpoke("cpspoke1")
	sc.Status.Health = &v1beta1.SpokeClusterHealth{Status: "Healthy", Phase: "Ready", PlanesHealthy: 2, PlanesTotal: 2}
	setCondition(&sc.Status, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionTrue, reasonSpokeReady, "ready")
	r := connectedReconciler(t, sc)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionFalse, reasonClusterAbsent)
	Expect(latest.Status.Health).To(BeNil(), "no stale health for a Cluster that is gone")
})

var _ = It("PullSpokeStatusKeepsPreviousHealthOnReadError", func() {
	t := GinkgoT()
	sc := connectableSpoke("cpspoke1")
	previous := &v1beta1.SpokeClusterHealth{Status: "Healthy", Phase: "Ready", PlanesHealthy: 2, PlanesTotal: 2}
	sc.Status.Health = previous.DeepCopy()
	setCondition(&sc.Status, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionTrue, reasonSpokeReady, "ready")
	r := connectedReconciler(t, sc)
	r.SpokeReader = interceptedSpokeReader(t, interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("proxy: 502 bad gateway")
		},
	})
	rec := &recordingRecorder{}
	r.record = rec

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred(), "a failed pull is state to report, not a controller fault")

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionSpokeReady, metav1.ConditionTrue, reasonSpokeReady)
	Expect(latest.Status.Health).To(Equal(previous), "the previous pull stands until the next one succeeds")

	pullFailures := 0
	for _, e := range rec.events {
		if e.Reason == reasonSpokeStatusPullFailed {
			pullFailures++
			Expect(e.Type).To(Equal(event.TypeWarning))
			Expect(e.Message).To(ContainSubstring("502"))
		}
	}
	Expect(pullFailures).To(Equal(1))
})

// lastPulledAt moves on every Connected pass; alone it must not force a status write, or
// the pull would reintroduce the per-interval etcd write the probe heartbeat avoids.
var _ = It("LastPulledAtAloneDoesNotForceStatusWrite", func() {
	pulled := metav1.NewTime(time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC))
	prev := v1beta1.SpokeClusterStatus{Health: &v1beta1.SpokeClusterHealth{Status: "Healthy", LastPulledAt: &pulled}}
	next := *prev.DeepCopy()
	later := metav1.NewTime(pulled.Add(30 * time.Second))
	next.Health.LastPulledAt = &later
	Expect(statusNeedsWrite(prev, next)).To(BeFalse())

	next.Health.Status = "Degraded"
	Expect(statusNeedsWrite(prev, next)).To(BeTrue(), "a real health change still writes")
})
