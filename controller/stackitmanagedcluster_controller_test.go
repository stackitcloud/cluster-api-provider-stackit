/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

func validManagedClusterSpec() infrav1.StackitManagedClusterSpec {
	return infrav1.StackitManagedClusterSpec{
		ProjectID:   testProjectID,
		Region:      "eu01",
		ClusterName: "ske-test",
		CredentialsSecretRef: corev1.SecretReference{
			Name: "stackit-credentials",
		},
		Kubernetes: infrav1.StackitManagedClusterKubernetesSpec{
			Version: "1.35.3",
		},
	}
}

func validManagedNodePoolSpec() infrav1.StackitManagedNodePoolSpec {
	return infrav1.StackitManagedNodePoolSpec{
		Name:              "np-default",
		AvailabilityZones: []string{"eu01-1"},
		Machine: infrav1.StackitManagedNodePoolMachineSpec{
			Type: "c2i.2",
			Image: infrav1.StackitManagedNodePoolImageSpec{
				Name:    "flatcar",
				Version: "4459.2.1",
			},
		},
		Minimum: 1,
		Maximum: 3,
		Volume: infrav1.StackitManagedNodePoolVolumeSpec{
			Size: 50,
			Type: "storage_premium_perf1",
		},
	}
}

var _ = Describe("StackitManagedCluster Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		managedCluster := &infrav1.StackitManagedCluster{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind StackitManagedCluster")
			err := k8sClient.Get(ctx, typeNamespacedName, managedCluster)
			if err != nil && errors.IsNotFound(err) {
				resource := &infrav1.StackitManagedCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: validManagedClusterSpec(),
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &infrav1.StackitManagedCluster{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("cleaning up the test StackitManagedCluster")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &StackitManagedClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should return nil when resource is not found", func() {
			controllerReconciler := &StackitManagedClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: "default",
					Name:      "non-existent",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When mapping Cluster events to StackitManagedCluster", func() {
		reconciler := &StackitManagedClusterReconciler{}

		It("should map to StackitManagedCluster request when infrastructureRef matches", func() {
			cluster := &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-cluster",
					Namespace: "my-namespace",
				},
				Spec: clusterv1.ClusterSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infrav1.GroupVersion.Group,
						Kind:     "StackitManagedCluster",
						Name:     "my-managed-cluster",
					},
				},
			}

			requests := reconciler.filterForStackitManagedClusterResources(context.Background(), cluster)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].NamespacedName).To(Equal(types.NamespacedName{
				Namespace: "my-namespace",
				Name:      "my-managed-cluster",
			}))
		})

		It("should ignore Clusters with different infrastructureRef Kind", func() {
			cluster := &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-cluster",
					Namespace: "my-namespace",
				},
				Spec: clusterv1.ClusterSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infrav1.GroupVersion.Group,
						Kind:     "StackitCluster",
						Name:     "my-stackit-cluster",
					},
				},
			}

			requests := reconciler.filterForStackitManagedClusterResources(context.Background(), cluster)
			Expect(requests).To(BeEmpty())
		})

		It("should ignore non-Cluster objects", func() {
			obj := &infrav1.StackitManagedCluster{}
			requests := reconciler.filterForStackitManagedClusterResources(context.Background(), obj)
			Expect(requests).To(BeNil())
		})
	})

	Context("StackitManagedCluster API validation", func() {
		newManagedCluster := func(name string) *infrav1.StackitManagedCluster {
			return &infrav1.StackitManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: validManagedClusterSpec(),
			}
		}

		It("accepts a valid minimal Spec", func() {
			cluster := newManagedCluster("valid-minimal-mc")
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, cluster) })
		})

		It("accepts optional network, labels, and controlPlaneEndpoint", func() {
			cluster := newManagedCluster("valid-optional-mc")
			cluster.Spec.Network = &infrav1.StackitManagedClusterNetworkSpec{
				ID: testNetworkID,
			}
			cluster.Spec.Labels = map[string]string{"environment": "dev"}
			cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{
				Host: "api.ske.example.com",
				Port: 6443,
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, cluster) })
		})

		It("rejects an invalid project ID", func() {
			cluster := newManagedCluster("invalid-project-mc")
			cluster.Spec.ProjectID = "not-a-uuid"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects an invalid region", func() {
			cluster := newManagedCluster("invalid-region-mc")
			cluster.Spec.Region = "eu-central-1"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects an invalid network ID when network is specified", func() {
			cluster := newManagedCluster("invalid-network-mc")
			cluster.Spec.Network = &infrav1.StackitManagedClusterNetworkSpec{
				ID: "invalid-network-id",
			}
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects missing credentials secret name", func() {
			cluster := newManagedCluster("missing-creds-mc")
			cluster.Spec.CredentialsSecretRef.Name = ""
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("credentialsSecretRef.name is required"))
		})

		It("accepts a valid 11-character cluster name", func() {
			cluster := newManagedCluster("valid-11char-mc")
			cluster.Spec.ClusterName = "ske-clust01"
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, cluster) })
		})

		It("accepts a 1-character cluster name", func() {
			cluster := newManagedCluster("valid-1char-mc")
			cluster.Spec.ClusterName = "a"
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, cluster) })
		})

		It("rejects a cluster name longer than 11 characters", func() {
			cluster := newManagedCluster("long-name-mc")
			cluster.Spec.ClusterName = "ske-cluster1"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects an uppercase cluster name", func() {
			cluster := newManagedCluster("uppercase-name-mc")
			cluster.Spec.ClusterName = "SkeCluster"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects a cluster name containing an underscore", func() {
			cluster := newManagedCluster("underscore-name-mc")
			cluster.Spec.ClusterName = "ske_cluster"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects a cluster name with a leading hyphen", func() {
			cluster := newManagedCluster("leading-hyphen-mc")
			cluster.Spec.ClusterName = "-skecluster"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects a cluster name with a trailing hyphen", func() {
			cluster := newManagedCluster("trailing-hyphen-mc")
			cluster.Spec.ClusterName = "skecluster-"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("accepts Kubernetes version 1.35.3", func() {
			cluster := newManagedCluster("valid-k8s-ver-mc")
			cluster.Spec.Kubernetes.Version = "1.35.3"
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, cluster) })
		})

		It("rejects Kubernetes version 1.35 without patch version", func() {
			cluster := newManagedCluster("short-k8s-ver-mc")
			cluster.Spec.Kubernetes.Version = "1.35"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects Kubernetes version v1.35.3 with leading v", func() {
			cluster := newManagedCluster("v-prefix-k8s-ver-mc")
			cluster.Spec.Kubernetes.Version = "v1.35.3"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects Kubernetes version latest", func() {
			cluster := newManagedCluster("latest-k8s-ver-mc")
			cluster.Spec.Kubernetes.Version = "latest"
			err := k8sClient.Create(ctx, cluster)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("validates status.initialization pointer shape and MinProperties=1", func() {
			cluster := newManagedCluster("init-status-mc")
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, cluster) })

			cluster.Status.Initialization = infrav1.StackitManagedClusterInitializationStatus{
				Provisioned: new(true),
			}
			Expect(k8sClient.Status().Update(ctx, cluster)).To(Succeed())

			got := &infrav1.StackitManagedCluster{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), got)).To(Succeed())
			Expect(got.Status.Initialization.Provisioned).NotTo(BeNil())
			Expect(*got.Status.Initialization.Provisioned).To(BeTrue())

			raw := &unstructured.Unstructured{}
			raw.SetAPIVersion(infrav1.GroupVersion.String())
			raw.SetKind("StackitManagedCluster")
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), raw)).To(Succeed())
			Expect(unstructured.SetNestedMap(raw.Object, map[string]any{}, "status", "initialization")).To(Succeed())
			Expect(errors.IsInvalid(k8sClient.Status().Update(ctx, raw))).To(BeTrue())
		})
	})

	Context("StackitManagedNodePool API validation", func() {
		newManagedNodePool := func(name string) *infrav1.StackitManagedNodePool {
			return &infrav1.StackitManagedNodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
				},
				Spec: validManagedNodePoolSpec(),
			}
		}

		newUnstructuredManagedNodePool := func(name string) *unstructured.Unstructured {
			u := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": infrav1.GroupVersion.String(),
					"kind":       "StackitManagedNodePool",
					"metadata": map[string]any{
						"name":      name,
						"namespace": "default",
					},
					"spec": map[string]any{
						"name":              "np-default",
						"availabilityZones": []any{"eu01-1"},
						"machine": map[string]any{
							"type": "c2i.2",
							"image": map[string]any{
								"name":    "flatcar",
								"version": "4459.2.1",
							},
						},
						"minimum": int64(1),
						"maximum": int64(3),
						"volume": map[string]any{
							"size": int64(50),
						},
					},
				},
			}
			return u
		}

		It("accepts a valid minimal required Spec", func() {
			pool := newManagedNodePool("valid-minimal-np")
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, pool) })
		})

		It("accepts optional node-pool fields and valid taints", func() {
			pool := newManagedNodePool("valid-optional-np")
			pool.Spec.AllowSystemComponents = new(true)
			pool.Spec.Labels = map[string]string{"role": "worker"}
			pool.Spec.MaxSurge = new(int32(2))
			pool.Spec.MaxUnavailable = new(int32(1))
			pool.Spec.Taints = []infrav1.StackitManagedNodePoolTaint{
				{
					Effect: corev1.TaintEffectNoSchedule,
					Key:    "dedicated",
					Value:  "gpu",
				},
			}
			pool.Spec.ProviderIDList = []string{"stackit://11111111-1111-1111-1111-111111111111"}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, pool) })
		})

		It("rejects a node-pool name over 15 characters", func() {
			pool := newManagedNodePool("long-name-np")
			pool.Spec.Name = "nodepool-too-long"
			err := k8sClient.Create(ctx, pool)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects missing required availabilityZones", func() {
			pool := newManagedNodePool("missing-azs-np")
			pool.Spec.AvailabilityZones = nil
			err := k8sClient.Create(ctx, pool)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects missing machine", func() {
			raw := newUnstructuredManagedNodePool("missing-machine-np")
			unstructured.RemoveNestedField(raw.Object, "spec", "machine")
			err := k8sClient.Create(ctx, raw)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects missing required machine image fields", func() {
			rawMissingImage := newUnstructuredManagedNodePool("missing-image-np")
			unstructured.RemoveNestedField(rawMissingImage.Object, "spec", "machine", "image")
			Expect(errors.IsInvalid(k8sClient.Create(ctx, rawMissingImage))).To(BeTrue())

			rawMissingImageName := newUnstructuredManagedNodePool("missing-image-name-np")
			unstructured.RemoveNestedField(rawMissingImageName.Object, "spec", "machine", "image", "name")
			Expect(errors.IsInvalid(k8sClient.Create(ctx, rawMissingImageName))).To(BeTrue())

			rawMissingImageVersion := newUnstructuredManagedNodePool("missing-image-ver-np")
			unstructured.RemoveNestedField(rawMissingImageVersion.Object, "spec", "machine", "image", "version")
			Expect(errors.IsInvalid(k8sClient.Create(ctx, rawMissingImageVersion))).To(BeTrue())
		})

		It("rejects missing volume", func() {
			raw := newUnstructuredManagedNodePool("missing-volume-np")
			unstructured.RemoveNestedField(raw.Object, "spec", "volume")
			err := k8sClient.Create(ctx, raw)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects node-pool labels exceeding MaxProperties=80", func() {
			pool := newManagedNodePool("too-many-labels-np")
			pool.Spec.Labels = make(map[string]string, 81)
			for i := range 81 {
				pool.Spec.Labels[fmt.Sprintf("k%d", i)] = "v"
			}
			Expect(errors.IsInvalid(k8sClient.Create(ctx, pool))).To(BeTrue())
		})

		It("rejects minimum greater than maximum", func() {
			pool := newManagedNodePool("min-gt-max-np")
			pool.Spec.Minimum = 5
			pool.Spec.Maximum = 3
			err := k8sClient.Create(ctx, pool)
			Expect(errors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("minimum must be less than or equal to maximum"))
		})

		It("rejects numeric limits outside verified SKE bounds", func() {
			poolMaxZero := newManagedNodePool("max-zero-np")
			poolMaxZero.Spec.Minimum = 0
			poolMaxZero.Spec.Maximum = 0
			Expect(errors.IsInvalid(k8sClient.Create(ctx, poolMaxZero))).To(BeTrue())

			poolMaxOver1000 := newManagedNodePool("max-over-1000-np")
			poolMaxOver1000.Spec.Maximum = 1001
			Expect(errors.IsInvalid(k8sClient.Create(ctx, poolMaxOver1000))).To(BeTrue())

			poolMinOver1000 := newManagedNodePool("min-over-1000-np")
			poolMinOver1000.Spec.Minimum = 1001
			poolMinOver1000.Spec.Maximum = 1000
			Expect(errors.IsInvalid(k8sClient.Create(ctx, poolMinOver1000))).To(BeTrue())

			poolVolTooSmall := newManagedNodePool("vol-small-np")
			poolVolTooSmall.Spec.Volume.Size = 19
			Expect(errors.IsInvalid(k8sClient.Create(ctx, poolVolTooSmall))).To(BeTrue())

			poolVolTooLarge := newManagedNodePool("vol-large-np")
			poolVolTooLarge.Spec.Volume.Size = 10241
			Expect(errors.IsInvalid(k8sClient.Create(ctx, poolVolTooLarge))).To(BeTrue())
		})

		It("rejects an invalid taint effect", func() {
			pool := newManagedNodePool("invalid-taint-np")
			pool.Spec.Taints = []infrav1.StackitManagedNodePoolTaint{
				{
					Effect: corev1.TaintEffect("InvalidEffect"),
					Key:    "dedicated",
				},
			}
			err := k8sClient.Create(ctx, pool)
			Expect(errors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects invalid providerIDList items violating CAPI contract bounds", func() {
			poolEmptyItem := newManagedNodePool("empty-providerid-np")
			poolEmptyItem.Spec.ProviderIDList = []string{""}
			Expect(errors.IsInvalid(k8sClient.Create(ctx, poolEmptyItem))).To(BeTrue())

			poolTooLongItem := newManagedNodePool("long-providerid-np")
			poolTooLongItem.Spec.ProviderIDList = []string{strings.Repeat("a", 513)}
			Expect(errors.IsInvalid(k8sClient.Create(ctx, poolTooLongItem))).To(BeTrue())
		})

		It("validates status.initialization pointer shape, MinProperties=1, and zero replicas serialization", func() {
			pool := newManagedNodePool("init-status-np")
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, pool) })

			pool.Status.Initialization = infrav1.StackitManagedNodePoolInitializationStatus{
				Provisioned: new(true),
			}
			pool.Status.Replicas = 0
			Expect(k8sClient.Status().Update(ctx, pool)).To(Succeed())

			got := &infrav1.StackitManagedNodePool{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pool), got)).To(Succeed())
			Expect(got.Status.Initialization.Provisioned).NotTo(BeNil())
			Expect(*got.Status.Initialization.Provisioned).To(BeTrue())
			Expect(got.Status.Replicas).To(Equal(int32(0)))

			raw := &unstructured.Unstructured{}
			raw.SetAPIVersion(infrav1.GroupVersion.String())
			raw.SetKind("StackitManagedNodePool")
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pool), raw)).To(Succeed())
			replicasVal, found, err := unstructured.NestedInt64(raw.Object, "status", "replicas")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(replicasVal).To(Equal(int64(0)))

			Expect(unstructured.SetNestedMap(raw.Object, map[string]any{}, "status", "initialization")).To(Succeed())
			Expect(errors.IsInvalid(k8sClient.Status().Update(ctx, raw))).To(BeTrue())
		})
	})
})
