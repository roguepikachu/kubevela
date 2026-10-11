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

package v1beta1

import (
	"context"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// splitYAMLDocuments splits a multi-document YAML file (separated by "---")
// into its individual document strings.
func splitYAMLDocuments(t GinkgoTInterface, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var docs []string
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		if strings.TrimSpace(doc) != "" {
			docs = append(docs, doc)
		}
	}
	return docs
}

// Every envtest spec in this package shares one control plane. Starting a
// kube-apiserver and etcd takes the better part of a minute, and nothing any spec
// does needs isolation from the others: they use distinct object names inside one
// namespace.
//
// ContinueOnFailure because Ordered otherwise skips the remaining specs once one
// fails, and these specs are independent. Without it a changed example document
// failing InstallAndApply would silently take the CEL rejection coverage with it.
var _ = Describe("SpokeClusterCRD envtest", Ordered, ContinueOnFailure, func() {
	var (
		testEnv   *envtest.Environment
		cfg       *rest.Config
		k8sClient client.Client
	)

	BeforeAll(func() {
		r := require.New(GinkgoT())
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			Skip("KUBEBUILDER_ASSETS not set; skipping envtest specs")
		}

		testEnv = &envtest.Environment{
			ControlPlaneStartTimeout: 2 * time.Minute,
			ControlPlaneStopTimeout:  time.Minute,
			UseExistingCluster:       ptr.To(false),
			CRDDirectoryPaths: []string{
				spokeClusterCRDPath, clusterPlaneCRDPath, clusterBlueprintCRDPath,
				clusterPlaneRevisionCRDPath, clusterBlueprintRevisionCRDPath, clusterCRDPath,
			},
		}

		var err error
		cfg, err = testEnv.Start()
		r.NoError(err, "envtest environment must start (requires KUBEBUILDER_ASSETS)")

		k8sClient, err = client.New(cfg, client.Options{Scheme: k8sscheme.Scheme})
		r.NoError(err)
		r.NoError(k8sClient.Create(context.Background(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "vela-system"},
		}))
	})

	AfterAll(func() {
		if testEnv != nil {
			require.NoError(GinkgoT(), testEnv.Stop())
		}
	})

	// Installs the generated SpokeCluster CRD, applies both worked examples, and
	// retrieves them back (Requirement 1, criterion 1; Requirement 7, criterion 1).
	It("InstallAndApply", func() {
		t := GinkgoT()
		r := require.New(t)
		ctx := context.Background()

		// The installed CRD must be namespaced: kubectl get spokeclusters lists
		// within a namespace, not cluster-wide (Requirement 1, criterion 3).
		disco, err := discovery.NewDiscoveryClientForConfig(cfg)
		r.NoError(err)
		resources, err := disco.ServerResourcesForGroupVersion(SchemeGroupVersion.String())
		r.NoError(err)
		var found bool
		for _, res := range resources.APIResources {
			if res.Name == "spokeclusters" {
				found = true
				r.True(res.Namespaced, "spokeclusters must be namespaced")
			}
		}
		r.True(found, "spokeclusters resource must be registered with the API server")

		// AWS Pod Identity example.
		awsDocs := splitYAMLDocuments(t, "../../../docs/examples/spokecluster-connect/spokecluster-aws.yaml")
		r.Len(awsDocs, 1)
		awsCluster := &SpokeCluster{}
		r.NoError(yaml.Unmarshal([]byte(awsDocs[0]), awsCluster))
		r.NoError(k8sClient.Create(ctx, awsCluster))

		gotAWS := &SpokeCluster{}
		r.NoError(k8sClient.Get(ctx, types.NamespacedName{Name: "prod-us-east-1", Namespace: "vela-system"}, gotAWS))
		r.Equal(SpokeClusterModeConnect, gotAWS.Spec.Mode)
		r.Equal(CredentialTypeAWS, gotAWS.Spec.Credential.Type)

		// Static kubeconfig example: a source Secret plus the SpokeCluster that
		// references it.
		kubeconfigDocs := splitYAMLDocuments(t, "../../../docs/examples/spokecluster-connect/spokecluster-kubeconfig.yaml")
		r.Len(kubeconfigDocs, 2)

		secret := &corev1.Secret{}
		r.NoError(yaml.Unmarshal([]byte(kubeconfigDocs[0]), secret))
		r.NoError(k8sClient.Create(ctx, secret))

		kubeconfigCluster := &SpokeCluster{}
		r.NoError(yaml.Unmarshal([]byte(kubeconfigDocs[1]), kubeconfigCluster))
		r.NoError(k8sClient.Create(ctx, kubeconfigCluster))

		gotKubeconfig := &SpokeCluster{}
		r.NoError(k8sClient.Get(ctx, types.NamespacedName{Name: "dev-spoke", Namespace: "vela-system"}, gotKubeconfig))
		r.Equal(CredentialTypeKubeconfig, gotKubeconfig.Spec.Credential.Type)
		r.Equal("dev-spoke-kubeconfig", gotKubeconfig.Spec.Credential.Kubeconfig.SecretRef.Name)

		// kubectl get spokeclusters must list both, cluster-wide retrieval within
		// the namespace (Requirement 1, criterion 1).
		list := &SpokeClusterList{}
		r.NoError(k8sClient.List(ctx, list, client.InNamespace("vela-system")))
		r.Len(list.Items, 2)
	})

	// The CEL rules have to be exercised against a real API server, because that is
	// the thing evaluating them. No webhook runs here, which is the point: the chart
	// ships the webhook off by default, so these rejections are what a default
	// install actually enforces at apply time.
	It("CELRejectsInvalidSpecs", func() {
		t := GinkgoT()
		r := require.New(t)
		ctx := context.Background()

		kubeconfigArm := func() *KubeconfigCredential {
			return &KubeconfigCredential{SecretRef: SecretKeyRef{Name: "some-kubeconfig"}}
		}
		awsArm := func() *AWSCredential {
			return &AWSCredential{
				AuthMode:    AWSAuthModePodIdentity,
				ClusterName: "spoke",
				Region:      "us-east-1",
				RoleARN:     "arn:aws:iam::111122223333:role/spoke",
				ExternalID:  "external",
			}
		}

		cases := []struct {
			name string
			want string
			spec SpokeClusterSpec
		}{
			{
				name: "arm-does-not-match-type",
				want: "when type is 'kubeconfig'",
				spec: SpokeClusterSpec{
					Mode:       SpokeClusterModeConnect,
					Credential: CredentialSpec{Type: CredentialTypeKubeconfig, AWS: awsArm()},
				},
			},
			{
				name: "no-arm-at-all",
				want: "when type is 'aws'",
				spec: SpokeClusterSpec{
					Mode:       SpokeClusterModeConnect,
					Credential: CredentialSpec{Type: CredentialTypeAWS},
				},
			},
			{
				name: "two-arms-set",
				want: "no other arm may be set",
				spec: SpokeClusterSpec{
					Mode: SpokeClusterModeConnect,
					Credential: CredentialSpec{
						Type: CredentialTypeKubeconfig, Kubeconfig: kubeconfigArm(), AWS: awsArm(),
					},
				},
			},
			{
				name: "phase-2-provisioning-in-connect-mode",
				want: "infraProvisioning is only read in modes 'provision' and 'adopt'",
				spec: SpokeClusterSpec{
					Mode:              SpokeClusterModeConnect,
					Credential:        CredentialSpec{Type: CredentialTypeKubeconfig, Kubeconfig: kubeconfigArm()},
					InfraProvisioning: &InfraProvisioning{BlueprintRef: &BlueprintReference{Name: "infra"}},
				},
			},
		}

		for _, tc := range cases {
			By(tc.name, func() {
				err := k8sClient.Create(ctx, &SpokeCluster{
					ObjectMeta: metav1.ObjectMeta{Name: tc.name, Namespace: "vela-system"},
					Spec:       tc.spec,
				})
				r.Error(err, "the API server must refuse %s with no webhook running", tc.name)
				r.Contains(err.Error(), tc.want, "the rejection has to name what is wrong")
			})
		}

		By("still admitting the Phase 2 stubs that are deliberately accepted", func() {
			// blueprintRef and rolloutStrategyRef are inert in Phase 1 but accepted, so
			// GitOps can land the Phase 2 shape early. Only infraProvisioning is refused.
			r.NoError(k8sClient.Create(ctx, &SpokeCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "accepted-stubs", Namespace: "vela-system"},
				Spec: SpokeClusterSpec{
					Mode:               SpokeClusterModeConnect,
					Credential:         CredentialSpec{Type: CredentialTypeKubeconfig, Kubeconfig: kubeconfigArm()},
					BlueprintRef:       &BlueprintReference{Name: "blueprint"},
					RolloutStrategyRef: &BlueprintReference{Name: "rollout"},
				},
			}))
		})

		By("refusing a connect spoke that tries to move straight to provision", func() {
			spoke := &SpokeCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "connect-to-provision", Namespace: "vela-system"},
				Spec: SpokeClusterSpec{
					Mode:       SpokeClusterModeConnect,
					Credential: CredentialSpec{Type: CredentialTypeKubeconfig, Kubeconfig: kubeconfigArm()},
				},
			}
			r.NoError(k8sClient.Create(ctx, spoke))
			spoke.Spec.Mode = SpokeClusterModeProvision
			spoke.Spec.InfraProvisioning = &InfraProvisioning{BlueprintRef: &BlueprintReference{Name: "infra"}}
			err := k8sClient.Update(ctx, spoke)
			r.Error(err, "connect to provision is not an allowed mode transition")
			r.Contains(err.Error(), "release", "the rejection has to spell out the allowed transitions")
		})

		By("letting a connect spoke adopt its cluster", func() {
			spoke := &SpokeCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "connect-to-adopt", Namespace: "vela-system"},
				Spec: SpokeClusterSpec{
					Mode:       SpokeClusterModeConnect,
					Credential: CredentialSpec{Type: CredentialTypeKubeconfig, Kubeconfig: kubeconfigArm()},
				},
			}
			r.NoError(k8sClient.Create(ctx, spoke))
			spoke.Spec.Mode = SpokeClusterModeAdopt
			spoke.Spec.InfraProvisioning = &InfraProvisioning{BlueprintRef: &BlueprintReference{Name: "infra"}}
			r.NoError(k8sClient.Update(ctx, spoke), "connect to adopt is an allowed mode transition")
		})
	})

	// The stored CRD has to carry the release counter through the status subresource,
	// otherwise the controller's per-incarnation Application name resets on every pass.
	It("StatusRoundTripsInfraIncarnation", func() {
		t := GinkgoT()
		r := require.New(t)
		ctx := context.Background()

		spoke := &SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "incarnation-counter", Namespace: "vela-system"},
			Spec: SpokeClusterSpec{
				Mode: SpokeClusterModeConnect,
				Credential: CredentialSpec{
					Type:       CredentialTypeKubeconfig,
					Kubeconfig: &KubeconfigCredential{SecretRef: SecretKeyRef{Name: "some-kubeconfig"}},
				},
			},
		}
		r.NoError(k8sClient.Create(ctx, spoke))
		spoke.Status.InfraIncarnation = 3
		r.NoError(k8sClient.Status().Update(ctx, spoke))

		got := &SpokeCluster{}
		r.NoError(k8sClient.Get(ctx, client.ObjectKeyFromObject(spoke), got))
		r.Equal(int32(3), got.Status.InfraIncarnation, "status.infraIncarnation must round-trip through the API server")
	})

	// spec.bootstrap names the ClusterPlane the hub renders into the spoke's runtime
	// install. It is valid in every mode, so a connect spoke carries it unchanged.
	It("AcceptsBootstrapPlaneRefInConnectMode", func() {
		r := require.New(GinkgoT())
		ctx := context.Background()

		r.NoError(k8sClient.Create(ctx, &SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-connect", Namespace: "vela-system"},
			Spec: SpokeClusterSpec{
				Mode: SpokeClusterModeConnect,
				Credential: CredentialSpec{
					Type:       CredentialTypeKubeconfig,
					Kubeconfig: &KubeconfigCredential{SecretRef: SecretKeyRef{Name: "some-kubeconfig"}},
				},
				Bootstrap: &BootstrapSpec{PlaneRef: PlaneReference{Name: "foundation-runtime"}},
			},
		}), "a connect spoke with spec.bootstrap.planeRef must be admitted")
	})

	// An empty bootstrap block says nothing about which plane to render, so the
	// schema refuses it even with the webhook off.
	It("RejectsBootstrapWithoutPlaneRef", func() {
		r := require.New(GinkgoT())
		ctx := context.Background()

		err := k8sClient.Create(ctx, &SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-empty", Namespace: "vela-system"},
			Spec: SpokeClusterSpec{
				Mode: SpokeClusterModeConnect,
				Credential: CredentialSpec{
					Type:       CredentialTypeKubeconfig,
					Kubeconfig: &KubeconfigCredential{SecretRef: SecretKeyRef{Name: "some-kubeconfig"}},
				},
				Bootstrap: &BootstrapSpec{},
			},
		})
		r.Error(err, "the API server must refuse spec.bootstrap without a planeRef")
		r.Contains(err.Error(), "planeRef", "the rejection has to name the missing field")
	})

	// status.bootstrap is the kubectl projection of the bootstrap Application and has
	// to survive the status subresource like status.provisioning does.
	It("StatusRoundTripsBootstrap", func() {
		r := require.New(GinkgoT())
		ctx := context.Background()

		spoke := &SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-status", Namespace: "vela-system"},
			Spec: SpokeClusterSpec{
				Mode: SpokeClusterModeConnect,
				Credential: CredentialSpec{
					Type:       CredentialTypeKubeconfig,
					Kubeconfig: &KubeconfigCredential{SecretRef: SecretKeyRef{Name: "some-kubeconfig"}},
				},
			},
		}
		r.NoError(k8sClient.Create(ctx, spoke))
		want := &ProvisioningStatus{ApplicationName: "sc-x-bootstrap", Healthy: true, Phase: "running", Message: "m"}
		spoke.Status.Bootstrap = want
		r.NoError(k8sClient.Status().Update(ctx, spoke))

		got := &SpokeCluster{}
		r.NoError(k8sClient.Get(ctx, client.ObjectKeyFromObject(spoke), got))
		r.Equal(want, got.Status.Bootstrap, "status.bootstrap must round-trip through the API server")
	})

	// A spoke Cluster names one blueprint revision per lifecycle phase, and the
	// hub reads its status back, so every status field has to survive the
	// subresource unchanged.
	It("ClusterAdmitsAllPhaseRefsAndRoundTripsStatus", func() {
		r := require.New(GinkgoT())
		ctx := context.Background()

		cluster := &Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "spoke-cluster", Namespace: "vela-system"},
			Spec: ClusterSpec{
				ClusterInit:       &PhaseSpec{BlueprintRef: BlueprintReference{Name: "init-abc12345"}},
				PlaneProvisioning: &PhaseSpec{BlueprintRef: BlueprintReference{Name: "planes-abc12345"}},
				HealthValidation:  &PhaseSpec{BlueprintRef: BlueprintReference{Name: "health-abc12345"}},
			},
		}
		r.NoError(k8sClient.Create(ctx, cluster), "a Cluster with all three phase refs must be admitted")

		now := metav1.NewTime(time.Now().Truncate(time.Second))
		wantStatus := ClusterStatus{
			ObservedGeneration: 1,
			Phase:              ClusterPhaseDegraded,
			Conditions: []metav1.Condition{{
				Type: ClusterConditionBlueprintResolved, Status: metav1.ConditionTrue,
				Reason: "Resolved", Message: "all revisions found", LastTransitionTime: now,
			}},
			Planes: []PlaneStatus{
				{Name: "network", Phase: LifecyclePhaseClusterInit, Application: "network-abc12345", Healthy: true},
				{Name: "storage", Phase: LifecyclePhasePlaneProvisioning, Application: "storage-abc12345", Healthy: false, Message: "pvc pending"},
			},
			Health: &ClusterHealth{Status: "Degraded", PlanesHealthy: 1, PlanesTotal: 2, LastCheckTime: &now},
		}
		cluster.Status = wantStatus
		r.NoError(k8sClient.Status().Update(ctx, cluster))

		got := &Cluster{}
		r.NoError(k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), got))
		r.Equal(wantStatus, got.Status, "Cluster status must round-trip through the status subresource")
	})

	// A Cluster that names no phase at all has nothing to run, so the schema
	// refuses it even with no webhook.
	It("ClusterRejectsSpecWithoutAnyPhaseRef", func() {
		r := require.New(GinkgoT())
		ctx := context.Background()

		err := k8sClient.Create(ctx, &Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "spoke-cluster-empty", Namespace: "vela-system"},
		})
		r.Error(err, "the API server must refuse a Cluster with no phase ref")
		r.Contains(err.Error(), "clusterInit", "the rejection has to name the phases one of which is required")
	})

	// Blueprints written before phases existed carry no phase; they keep working
	// as plain plane provisioning. Anything outside the enum is a typo and is
	// refused.
	It("BlueprintPlanePhaseDefaultsAndIsClosed", func() {
		r := require.New(GinkgoT())
		ctx := context.Background()

		blueprint := &ClusterBlueprint{
			ObjectMeta: metav1.ObjectMeta{Name: "phase-default", Namespace: "vela-system"},
			Spec: ClusterBlueprintSpec{Planes: []BlueprintPlane{
				{Name: "network", Ref: BlueprintReference{Name: "network"}},
			}},
		}
		r.NoError(k8sClient.Create(ctx, blueprint))
		got := &ClusterBlueprint{}
		r.NoError(k8sClient.Get(ctx, client.ObjectKeyFromObject(blueprint), got))
		r.Equal(LifecyclePhasePlaneProvisioning, got.Spec.Planes[0].Phase, "a plane without a phase is plane provisioning")

		err := k8sClient.Create(ctx, &ClusterBlueprint{
			ObjectMeta: metav1.ObjectMeta{Name: "phase-nonsense", Namespace: "vela-system"},
			Spec: ClusterBlueprintSpec{Planes: []BlueprintPlane{
				{Name: "network", Ref: BlueprintReference{Name: "network"}, Phase: LifecyclePhase("nonsense")},
			}},
		})
		r.Error(err, "the API server must refuse a phase outside the lifecycle enum")
		r.Contains(err.Error(), "phase", "the rejection has to name the field")
	})

	// The two revision kinds are what the hub ships to the spoke; both have to
	// store and read back with their labels and pinned references intact.
	It("RevisionsRoundTrip", func() {
		r := require.New(GinkgoT())
		ctx := context.Background()

		planeRev := &ClusterPlaneRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name: "network-abc12345", Namespace: "vela-system",
				Labels: map[string]string{LabelPlaneName: "network", LabelPlaneHash: "abc12345"},
			},
			Spec: ClusterPlaneRevisionSpec{
				PlaneName: "network", Hash: "abc12345", Description: "cni and dns",
				Components: []ClusterPlaneComponent{{Name: "cilium", Type: "helm"}},
			},
		}
		r.NoError(k8sClient.Create(ctx, planeRev))
		gotPlane := &ClusterPlaneRevision{}
		r.NoError(k8sClient.Get(ctx, client.ObjectKeyFromObject(planeRev), gotPlane))
		r.Equal(planeRev.Spec, gotPlane.Spec)
		r.Equal("network", gotPlane.Labels[LabelPlaneName])

		blueprintRev := &ClusterBlueprintRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name: "base-def67890", Namespace: "vela-system",
				Labels: map[string]string{LabelBlueprintName: "base", LabelBlueprintHash: "def67890"},
			},
			Spec: ClusterBlueprintRevisionSpec{
				BlueprintName: "base", Hash: "def67890",
				Planes: []BlueprintPlaneRevision{
					{Name: "network", Phase: LifecyclePhaseClusterInit, PlaneRevision: "network-abc12345"},
					{Name: "storage", Phase: LifecyclePhasePlaneProvisioning, PlaneRevision: "storage-abc12345", DependsOn: []string{"network"}},
				},
			},
		}
		r.NoError(k8sClient.Create(ctx, blueprintRev))
		gotBlueprint := &ClusterBlueprintRevision{}
		r.NoError(k8sClient.Get(ctx, client.ObjectKeyFromObject(blueprintRev), gotBlueprint))
		r.Equal(blueprintRev.Spec, gotBlueprint.Spec)

		err := k8sClient.Create(ctx, &ClusterBlueprintRevision{
			ObjectMeta: metav1.ObjectMeta{Name: "base-nophase", Namespace: "vela-system"},
			Spec: ClusterBlueprintRevisionSpec{
				BlueprintName: "base", Hash: "nophase0",
				Planes: []BlueprintPlaneRevision{{Name: "network", PlaneRevision: "network-abc12345"}},
			},
		})
		r.Error(err, "a snapshot has to record each plane's phase explicitly")
		r.Contains(err.Error(), "phase")
	})
})
