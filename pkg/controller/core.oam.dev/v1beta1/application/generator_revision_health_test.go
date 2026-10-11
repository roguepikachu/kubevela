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

package application

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/util"
	"github.com/oam-dev/kubevela/pkg/resourcekeeper"
)

// rootTrackedKeeper wraps a real ResourceKeeper and reports every resource as already tracked,
// which is what a garbage-collect policy with strategy never or onAppDelete produces across
// Application revisions. Policy lookups are delegated to the wrapped keeper.
type rootTrackedKeeper struct{ resourcekeeper.ResourceKeeper }

func (rootTrackedKeeper) ContainsResources([]*unstructured.Unstructured) bool { return true }

// policyStubKeeper is a ResourceKeeper double for the unit specs: only the two policy lookups are
// implemented, everything else panics through the nil embedded interface.
type policyStubKeeper struct {
	resourcekeeper.ResourceKeeper
	applyOnceAll bool
	readOnly     bool
}

func (k policyStubKeeper) IsApplyOnceAll(*unstructured.Unstructured) bool { return k.applyOnceAll }
func (k policyStubKeeper) IsReadOnly(*unstructured.Unstructured) bool     { return k.readOnly }

var _ = Describe("Deploy health check across application revisions", func() {
	const (
		compName   = "revcheck"
		currentRev = "rev-health-app-v2"
		olderRev   = "rev-health-app-v1"
	)

	var (
		ctx           context.Context
		namespaceName string
		app           *v1beta1.Application
	)

	BeforeEach(func() {
		namespaceName = "rev-health-" + rand.String(8)
		ctx = context.WithValue(context.TODO(), util.AppDefinitionNamespace, namespaceName)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())

		def := &v1beta1.ComponentDefinition{}
		raw, err := yaml.YAMLToJSON([]byte(cdDefWithHealthStatusYaml))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(raw, def)).To(Succeed())
		def.Name = "worker-with-health"
		def.Namespace = namespaceName
		Expect(k8sClient.Create(ctx, def)).To(Succeed())

		app = &v1beta1.Application{
			TypeMeta:   metav1.TypeMeta{Kind: "Application", APIVersion: "core.oam.dev/v1beta1"},
			ObjectMeta: metav1.ObjectMeta{Name: "rev-health-app", Namespace: namespaceName},
			Spec: v1beta1.ApplicationSpec{
				Components: []common.ApplicationComponent{{
					Name:       compName,
					Type:       "worker-with-health",
					Properties: &runtime.RawExtension{Raw: []byte(`{"image":"busybox","lives":"3","enemies":"none"}`)},
				}},
			},
		}
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(context.TODO(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())
	})

	// liveLabels builds the labels the dispatcher stamps on a resource owned by ownerApp. An empty
	// appRevName leaves the appRevision label out, as on an adopted object.
	liveLabels := func(ownerApp, appRevName string, extra map[string]string) map[string]string {
		labels := map[string]string{
			oam.LabelAppName:      ownerApp,
			oam.LabelAppNamespace: namespaceName,
			oam.LabelAppComponent: compName,
		}
		if appRevName != "" {
			labels[oam.LabelAppRevision] = appRevName
		}
		for k, v := range extra {
			labels[k] = v
		}
		return labels
	}

	// createLiveWorkload creates the Deployment and ConfigMap the component renders, labelled as
	// dispatched by ownerApp at appRevName, with a status the healthPolicy evaluates as healthy.
	createLiveWorkload := func(ownerApp, appRevName string) {
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      compName,
				Namespace: namespaceName,
				Labels:    liveLabels(ownerApp, appRevName, map[string]string{oam.LabelOAMResourceType: oam.ResourceTypeWorkload}),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{oam.LabelAppComponent: compName}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{oam.LabelAppComponent: compName}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: compName, Image: "busybox"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, deploy)).To(Succeed())
		deploy.Status = appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1}
		Expect(k8sClient.Status().Update(ctx, deploy)).To(Succeed())

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      compName + "game-config",
				Namespace: namespaceName,
				Labels:    liveLabels(ownerApp, appRevName, map[string]string{oam.TraitResource: "gameconfig"}),
			},
			Data: map[string]string{"enemies": "none", "lives": "3"},
		}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
	}

	// runHealthCheck runs the deploy step's health check for the component as revision currentRev,
	// with the resource keeper reporting the component's resources as already tracked.
	runHealthCheck := func() (bool, *common.ApplicationComponentStatus) {
		af, err := appParser.GenerateAppFile(ctx, app)
		Expect(err).NotTo(HaveOccurred())
		handler, err := NewAppHandler(ctx, reconciler, app)
		Expect(err).NotTo(HaveOccurred())
		handler.resourceKeeper = rootTrackedKeeper{handler.resourceKeeper}
		handler.currentAppRev = &v1beta1.ApplicationRevision{
			ObjectMeta: metav1.ObjectMeta{Name: currentRev, Namespace: namespaceName},
			Spec:       v1beta1.ApplicationRevisionSpec{ApplicationRevisionCompressibleFields: v1beta1.ApplicationRevisionCompressibleFields{Application: *app}},
		}
		healthy, status, _, _, err := handler.checkComponentHealth(appParser, af)(ctx, app.Spec.Components[0], nil, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(status).NotTo(BeNil())
		return healthy, status
	}

	applyOncePolicy := func(properties string) {
		app.Spec.Policies = []v1beta1.AppPolicy{{
			Name:       "apply-once",
			Type:       "apply-once",
			Properties: &runtime.RawExtension{Raw: []byte(properties)},
		}}
	}

	expectPendingReapply := func(status *common.ApplicationComponentStatus) {
		Expect(status.Healthy).To(BeFalse())
		Expect(status.WorkloadHealthy).To(BeTrue())
		Expect(status.Message).To(HavePrefix("type: busybox"))
		Expect(status.Message).To(ContainSubstring("applied by revision " + olderRev))
		Expect(status.Message).To(ContainSubstring("current revision is " + currentRev))
		Expect(status.Message).To(ContainSubstring("pending re-apply"))
	}

	expectHealthy := func(healthy bool, status *common.ApplicationComponentStatus) {
		Expect(healthy).To(BeTrue())
		Expect(status.Healthy).To(BeTrue())
		Expect(status.Message).NotTo(ContainSubstring("pending re-apply"))
	}

	It("reports a healthy owned workload left by an older revision as not yet applied", func() {
		createLiveWorkload(app.Name, olderRev)

		healthy, status := runHealthCheck()

		Expect(healthy).To(BeFalse())
		expectPendingReapply(status)
	})

	It("keeps a healthy workload applied by the current revision healthy", func() {
		createLiveWorkload(app.Name, currentRev)

		expectHealthy(runHealthCheck())
	})

	It("leaves a workload without an appRevision label alone", func() {
		createLiveWorkload(app.Name, "")

		expectHealthy(runHealthCheck())
	})

	It("skips a workload controlled by another Application", func() {
		createLiveWorkload("other-app", olderRev)

		expectHealthy(runHealthCheck())
	})

	It("skips an owned workload kept whole by an apply-once rule with path *", func() {
		applyOncePolicy(`{"enable":true,"rules":[{"selector":{"resourceTypes":["Deployment","ConfigMap"]},"strategy":{"path":["*"]}}]}`)
		createLiveWorkload(app.Name, olderRev)

		expectHealthy(runHealthCheck())
	})

	It("still flags an owned workload under apply-once without rules", func() {
		applyOncePolicy(`{"enable":true}`)
		createLiveWorkload(app.Name, olderRev)

		healthy, status := runHealthCheck()

		Expect(healthy).To(BeFalse())
		expectPendingReapply(status)
	})
})

var _ = Describe("staleRevisionResource", func() {
	const ownerNs, ownerName = "demo", "owner"

	owned := func(kind, name, rev string) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetKind(kind)
		obj.SetName(name)
		labels := map[string]string{oam.LabelAppName: ownerName, oam.LabelAppNamespace: ownerNs}
		if rev != "" {
			labels[oam.LabelAppRevision] = rev
		}
		obj.SetLabels(labels)
		return obj
	}

	newHandler := func(keeper resourcekeeper.ResourceKeeper, currentRev string) *AppHandler {
		return &AppHandler{
			app:            &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: ownerName, Namespace: ownerNs}},
			currentAppRev:  &v1beta1.ApplicationRevision{ObjectMeta: metav1.ObjectMeta{Name: currentRev}},
			resourceKeeper: keeper,
		}
	}

	It("returns the first owned live object labelled with another revision", func() {
		h := newHandler(policyStubKeeper{}, "app-v2")
		stale, found := h.staleRevisionResource(owned("Deployment", "web", "app-v2"), owned("ConfigMap", "web-config", "app-v1"))
		Expect(found).To(BeTrue())
		Expect(stale.GetName()).To(Equal("web-config"))
	})

	It("ignores nil, unlabelled and empty objects", func() {
		h := newHandler(policyStubKeeper{}, "app-v2")
		_, found := h.staleRevisionResource(nil, owned("Deployment", "web", ""), &unstructured.Unstructured{})
		Expect(found).To(BeFalse())
	})

	It("ignores objects controlled by another Application", func() {
		foreign := owned("Deployment", "web", "app-v1")
		foreign.SetLabels(map[string]string{oam.LabelAppName: "someone-else", oam.LabelAppNamespace: ownerNs, oam.LabelAppRevision: "app-v1"})
		h := newHandler(policyStubKeeper{}, "app-v2")
		_, found := h.staleRevisionResource(foreign)
		Expect(found).To(BeFalse())
	})

	It("ignores objects an apply-once * rule or a read-only rule keeps as they are", func() {
		stale := owned("Deployment", "web", "app-v1")
		_, found := newHandler(policyStubKeeper{applyOnceAll: true}, "app-v2").staleRevisionResource(stale)
		Expect(found).To(BeFalse())
		_, found = newHandler(policyStubKeeper{readOnly: true}, "app-v2").staleRevisionResource(stale)
		Expect(found).To(BeFalse())
	})

	It("does nothing without a current revision name", func() {
		h := newHandler(policyStubKeeper{}, "")
		_, found := h.staleRevisionResource(owned("Deployment", "web", "app-v1"))
		Expect(found).To(BeFalse())
	})
})
