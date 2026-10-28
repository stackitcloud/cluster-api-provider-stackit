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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

var _ = Describe("StackitManagedCluster Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		stackitmanagedcluster := &infrav1.StackitManagedCluster{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind StackitManagedCluster")
			err := k8sClient.Get(ctx, typeNamespacedName, stackitmanagedcluster)
			if err != nil && errors.IsNotFound(err) {
				resource := &infrav1.StackitManagedCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					// TODO(user): Specify other spec details if needed.
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &infrav1.StackitManagedCluster{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance StackitManagedCluster")
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
})
