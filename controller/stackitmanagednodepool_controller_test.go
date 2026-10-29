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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
)

var _ = Describe("StackitManagedNodePool Controller", func() {
	const namespace = "default"

	createManagedClusterChain := func(
		clusterName, managedClusterName, machinePoolName, nodePoolName string,
	) (*clusterv1.Cluster, *infrav1.StackitManagedCluster, *clusterv1.MachinePool, *infrav1.StackitManagedNodePool) {
		managedCluster := &infrav1.StackitManagedCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      managedClusterName,
				Namespace: namespace,
			},
			Spec: validManagedClusterSpec(),
		}
		Expect(k8sClient.Create(ctx, managedCluster)).To(Succeed())
		DeferCleanup(func() { deleteIfExists(ctx, managedCluster) })

		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: namespace,
			},
			Spec: clusterv1.ClusterSpec{
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: infrav1.GroupVersion.Group,
					Kind:     "StackitManagedCluster",
					Name:     managedClusterName,
				},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		DeferCleanup(func() { deleteIfExists(ctx, cluster) })

		machinePool := &clusterv1.MachinePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machinePoolName,
				Namespace: namespace,
				Labels: map[string]string{
					clusterv1.ClusterNameLabel: clusterName,
				},
			},
			Spec: clusterv1.MachinePoolSpec{
				ClusterName: clusterName,
				Template: clusterv1.MachineTemplateSpec{
					Spec: clusterv1.MachineSpec{
						ClusterName: clusterName,
						Bootstrap: clusterv1.Bootstrap{
							DataSecretName: new(""),
						},
						InfrastructureRef: clusterv1.ContractVersionedObjectReference{
							APIGroup: infrav1.GroupVersion.Group,
							Kind:     "StackitManagedNodePool",
							Name:     nodePoolName,
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, machinePool)).To(Succeed())
		DeferCleanup(func() { deleteIfExists(ctx, machinePool) })

		nodePool := &infrav1.StackitManagedNodePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nodePoolName,
				Namespace: namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: clusterv1.GroupVersion.String(),
						Kind:       "MachinePool",
						Name:       machinePool.Name,
						UID:        machinePool.UID,
					},
				},
			},
			Spec: validManagedNodePoolSpec(),
		}
		Expect(k8sClient.Create(ctx, nodePool)).To(Succeed())
		DeferCleanup(func() { deleteIfExists(ctx, nodePool) })

		return cluster, managedCluster, machinePool, nodePool
	}

	Context("When reconciling a StackitManagedNodePool", func() {
		It("returns nil when the resource is not found", func() {
			reconciler := &StackitManagedNodePoolReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: namespace,
					Name:      "non-existent-nodepool",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns nil without fabricating status when no owning MachinePool exists", func() {
			nodePool := &infrav1.StackitManagedNodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "orphan-nodepool",
					Namespace: namespace,
				},
				Spec: validManagedNodePoolSpec(),
			}
			Expect(k8sClient.Create(ctx, nodePool)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, nodePool) })

			reconciler := &StackitManagedNodePoolReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(nodePool),
			})
			Expect(err).NotTo(HaveOccurred())

			got := &infrav1.StackitManagedNodePool{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodePool), got)).To(Succeed())
			Expect(got.Status.Ready).To(BeFalse())
			Expect(got.Status.Initialization.Provisioned).To(BeNil())
			Expect(got.Status.Replicas).To(BeZero())
			Expect(got.Spec.ProviderIDList).To(BeEmpty())
			Expect(got.Finalizers).To(BeEmpty())
		})

		It("resolves MachinePool, Cluster, and StackitManagedCluster without mutating status or adding finalizers", func() {
			_, _, _, nodePool := createManagedClusterChain(
				"np-reconcile-cluster",
				"np-reconcile-mc",
				"np-reconcile-mp",
				"np-reconcile-pool",
			)

			reconciler := &StackitManagedNodePoolReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(nodePool),
			})
			Expect(err).NotTo(HaveOccurred())

			got := &infrav1.StackitManagedNodePool{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodePool), got)).To(Succeed())
			Expect(got.Status.Ready).To(BeFalse())
			Expect(got.Status.Initialization.Provisioned).To(BeNil())
			Expect(got.Status.Replicas).To(BeZero())
			Expect(got.Spec.ProviderIDList).To(BeEmpty())
			Expect(got.Finalizers).To(BeEmpty())
		})
	})

	Context("When mapping MachinePool events to StackitManagedNodePool", func() {
		reconciler := &StackitManagedNodePoolReconciler{}

		It("enqueues the referenced StackitManagedNodePool when infrastructureRef matches", func() {
			machinePool := &clusterv1.MachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mp-0",
					Namespace: namespace,
				},
				Spec: clusterv1.MachinePoolSpec{
					ClusterName: "my-cluster",
					Template: clusterv1.MachineTemplateSpec{
						Spec: clusterv1.MachineSpec{
							InfrastructureRef: clusterv1.ContractVersionedObjectReference{
								APIGroup: infrav1.GroupVersion.Group,
								Kind:     "StackitManagedNodePool",
								Name:     "my-nodepool",
							},
						},
					},
				},
			}

			requests := reconciler.stackitManagedNodePoolRequestsForMachinePool(context.Background(), machinePool)
			Expect(requests).To(Equal([]reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Namespace: namespace,
					Name:      "my-nodepool",
				},
			}}))
		})

		It("ignores MachinePools with unrelated infrastructureRef Kind", func() {
			machinePool := &clusterv1.MachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mp-0",
					Namespace: namespace,
				},
				Spec: clusterv1.MachinePoolSpec{
					ClusterName: "my-cluster",
					Template: clusterv1.MachineTemplateSpec{
						Spec: clusterv1.MachineSpec{
							InfrastructureRef: clusterv1.ContractVersionedObjectReference{
								APIGroup: infrav1.GroupVersion.Group,
								Kind:     "StackitMachineTemplate",
								Name:     "unmanaged-template",
							},
						},
					},
				},
			}

			requests := reconciler.stackitManagedNodePoolRequestsForMachinePool(context.Background(), machinePool)
			Expect(requests).To(BeEmpty())
		})
	})

	Context("When mapping StackitManagedNodePool events to StackitManagedCluster", func() {
		It("enqueues the owning StackitManagedCluster via MachinePool and Cluster", func() {
			_, managedCluster, _, nodePool := createManagedClusterChain(
				"map-chain-cluster",
				"map-chain-mc",
				"map-chain-mp",
				"map-chain-np",
			)

			clusterReconciler := &StackitManagedClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			requests := clusterReconciler.stackitManagedClusterRequestsForNodePool(ctx, nodePool)
			Expect(requests).To(Equal([]reconcile.Request{{
				NamespacedName: client.ObjectKeyFromObject(managedCluster),
			}}))
		})

		It("does not enqueue anything when the Cluster references an unmanaged StackitCluster", func() {
			unmanagedCluster := &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unmanaged-capi-cluster",
					Namespace: namespace,
				},
				Spec: clusterv1.ClusterSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infrav1.GroupVersion.Group,
						Kind:     "StackitCluster",
						Name:     "unmanaged-stackit-cluster",
					},
				},
			}
			Expect(k8sClient.Create(ctx, unmanagedCluster)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, unmanagedCluster) })

			machinePool := &clusterv1.MachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unmanaged-mp",
					Namespace: namespace,
					Labels: map[string]string{
						clusterv1.ClusterNameLabel: unmanagedCluster.Name,
					},
				},
				Spec: clusterv1.MachinePoolSpec{
					ClusterName: unmanagedCluster.Name,
					Template: clusterv1.MachineTemplateSpec{
						Spec: clusterv1.MachineSpec{
							ClusterName: unmanagedCluster.Name,
							Bootstrap: clusterv1.Bootstrap{
								DataSecretName: new(""),
							},
							InfrastructureRef: clusterv1.ContractVersionedObjectReference{
								APIGroup: infrav1.GroupVersion.Group,
								Kind:     "StackitManagedNodePool",
								Name:     "unmanaged-np",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, machinePool)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, machinePool) })

			nodePool := &infrav1.StackitManagedNodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unmanaged-np",
					Namespace: namespace,
				},
				Spec: validManagedNodePoolSpec(),
			}
			Expect(k8sClient.Create(ctx, nodePool)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, nodePool) })

			clusterReconciler := &StackitManagedClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			requests := clusterReconciler.stackitManagedClusterRequestsForNodePool(ctx, nodePool)
			Expect(requests).To(BeEmpty())
		})

		It("does not enqueue anything for an unassociated StackitManagedNodePool", func() {
			nodePool := &infrav1.StackitManagedNodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unlinked-np",
					Namespace: namespace,
				},
				Spec: validManagedNodePoolSpec(),
			}
			Expect(k8sClient.Create(ctx, nodePool)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, nodePool) })

			clusterReconciler := &StackitManagedClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			requests := clusterReconciler.stackitManagedClusterRequestsForNodePool(ctx, nodePool)
			Expect(requests).To(BeEmpty())
		})
	})
})
