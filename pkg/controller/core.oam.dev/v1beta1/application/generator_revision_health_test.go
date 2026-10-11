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
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/util"
	"github.com/oam-dev/kubevela/pkg/resourcekeeper"
)

// rootTrackedKeeper stands in for a ResourceKeeper whose root ResourceTracker already records
// every resource of the component, which is what a garbage-collect policy with strategy never
// or onAppDelete produces across Application revisions.
type rootTrackedKeeper struct{ resourcekeeper.ResourceKeeper }

func (rootTrackedKeeper) ContainsResources([]*unstructured.Unstructured) bool { return true }

var _ = Describe("Deploy health check across application revisions", func() {
	const compName = "revcheck"

	var (
		ctx           context.Context
		namespaceName string
		app           *v1beta1.Application
	)

	BeforeEach(func() {
		namespaceName = "rev-health-" + strconv.Itoa(time.Now().Second()) + "-" + strconv.Itoa(time.Now().Nanosecond())
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

	// createLiveWorkload creates the Deployment and ConfigMap the component renders, labelled as
	// dispatched by appRevName, with a status the definition's healthPolicy evaluates as healthy.
	createLiveWorkload := func(appRevName string) {
		labels := map[string]string{
			oam.LabelAppName:         app.Name,
			oam.LabelAppComponent:    compName,
			oam.LabelAppRevision:     appRevName,
			oam.LabelOAMResourceType: oam.ResourceTypeWorkload,
		}
		deploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: compName, Namespace: namespaceName, Labels: labels},
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
				Labels: map[string]string{
					oam.LabelAppName:      app.Name,
					oam.LabelAppComponent: compName,
					oam.LabelAppRevision:  appRevName,
					oam.TraitResource:     "gameconfig",
				},
			},
			Data: map[string]string{"enemies": "none", "lives": "3"},
		}
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
	}

	// runHealthCheck runs the deploy step's health check for the component as revision currentRevName,
	// with the resource keeper reporting the component's resources as already tracked.
	runHealthCheck := func(currentRevName string) (bool, *common.ApplicationComponentStatus) {
		af, err := appParser.GenerateAppFile(ctx, app)
		Expect(err).NotTo(HaveOccurred())
		handler, err := NewAppHandler(ctx, reconciler, app)
		Expect(err).NotTo(HaveOccurred())
		handler.resourceKeeper = rootTrackedKeeper{handler.resourceKeeper}
		handler.currentAppRev = &v1beta1.ApplicationRevision{
			ObjectMeta: metav1.ObjectMeta{Name: currentRevName, Namespace: namespaceName},
			Spec:       v1beta1.ApplicationRevisionSpec{ApplicationRevisionCompressibleFields: v1beta1.ApplicationRevisionCompressibleFields{Application: *app}},
		}
		healthy, status, _, _, err := handler.checkComponentHealth(appParser, af)(ctx, app.Spec.Components[0], nil, "", "")
		Expect(err).NotTo(HaveOccurred())
		return healthy, status
	}

	It("reports a healthy workload left by an older revision as not yet applied", func() {
		createLiveWorkload("rev-health-app-v1")

		healthy, status := runHealthCheck("rev-health-app-v2")

		Expect(healthy).To(BeFalse())
		Expect(status).NotTo(BeNil())
		Expect(status.Healthy).To(BeFalse())
		Expect(status.Message).To(ContainSubstring("applied by revision rev-health-app-v1"))
		Expect(status.Message).To(ContainSubstring("current revision is rev-health-app-v2"))
		Expect(status.Message).To(ContainSubstring("pending re-apply"))
	})

	It("keeps a healthy workload applied by the current revision healthy", func() {
		createLiveWorkload("rev-health-app-v2")

		healthy, status := runHealthCheck("rev-health-app-v2")

		Expect(healthy).To(BeTrue())
		Expect(status).NotTo(BeNil())
		Expect(status.Healthy).To(BeTrue())
		Expect(status.Message).NotTo(ContainSubstring("pending re-apply"))
	})
})

var _ = Describe("appliedByOtherRevision", func() {
	labelled := func(kind, name, rev string) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetKind(kind)
		obj.SetName(name)
		if rev != "" {
			obj.SetLabels(map[string]string{oam.LabelAppRevision: rev})
		}
		return obj
	}

	It("returns the first live object labelled with another revision", func() {
		stale, found := appliedByOtherRevision("app-v2",
			labelled("Deployment", "web", "app-v2"),
			labelled("ConfigMap", "web-config", "app-v1"),
		)
		Expect(found).To(BeTrue())
		Expect(stale.GetName()).To(Equal("web-config"))
	})

	It("ignores unlabelled and nil objects", func() {
		_, found := appliedByOtherRevision("app-v2", nil, labelled("Deployment", "web", ""), &unstructured.Unstructured{})
		Expect(found).To(BeFalse())
	})

	It("does nothing without a current revision name", func() {
		_, found := appliedByOtherRevision("", labelled("Deployment", "web", "app-v1"))
		Expect(found).To(BeFalse())
	})
})
