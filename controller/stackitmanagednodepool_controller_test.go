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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
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
		DeferCleanup(func() {
			got := &infrav1.StackitManagedCluster{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(managedCluster), got); err == nil {
				got.Finalizers = nil
				_ = k8sClient.Update(ctx, got)
				deleteIfExists(ctx, got)
			}
		})

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

		managedCluster.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "Cluster",
			Name:       cluster.Name,
			UID:        cluster.UID,
		}}
		Expect(k8sClient.Update(ctx, managedCluster)).To(Succeed())

		machinePool := &clusterv1.MachinePool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machinePoolName,
				Namespace: namespace,
				Labels: map[string]string{
					clusterv1.ClusterNameLabel: clusterName,
				},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Cluster",
					Name:       cluster.Name,
					UID:        cluster.UID,
				}},
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
				Labels: map[string]string{
					clusterv1.ClusterNameLabel: clusterName,
				},
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
		DeferCleanup(func() {
			got := &infrav1.StackitManagedNodePool{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(nodePool), got); err == nil {
				got.Finalizers = nil
				_ = k8sClient.Update(ctx, got)
				deleteIfExists(ctx, got)
			}
		})

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

		It("does not add a finalizer or fabricate status when no owning MachinePool exists", func() {
			nodePool := &infrav1.StackitManagedNodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("orphan-np-%d", time.Now().UnixNano()),
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

		It("adds ManagedNodePoolFinalizer and sets Paused=False without touching cloud-owned status fields", func() {
			suffix := fmt.Sprintf("%d", time.Now().UnixNano())
			_, _, _, nodePool := createManagedClusterChain(
				"np-cl-"+suffix,
				"np-mc-"+suffix,
				"np-mp-"+suffix,
				"np-pool-"+suffix,
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
			Expect(got.Finalizers).To(ContainElement(infrav1.ManagedNodePoolFinalizer))
			Expect(got.Status.Ready).To(BeFalse())
			Expect(got.Status.Initialization.Provisioned).To(BeNil())
			Expect(got.Status.Replicas).To(BeZero())
			Expect(got.Spec.ProviderIDList).To(BeEmpty())
			expectCondition(got.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionFalse, clusterv1.NotPausedReason)
		})

		It("respects pause annotations during normal reconciliation and bypasses pause during parent-cluster deletion", func() {
			suffix := fmt.Sprintf("%d", time.Now().UnixNano())
			cluster, managedCluster, _, nodePool := createManagedClusterChain(
				"pause-cl-"+suffix,
				"pause-mc-"+suffix,
				"pause-mp-"+suffix,
				"pause-pool-"+suffix,
			)

			reconciler := &StackitManagedNodePoolReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// First reconcile adds finalizer.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(nodePool),
			})
			Expect(err).NotTo(HaveOccurred())

			// Pause the Cluster and reconcile StackitManagedNodePool -> sets Paused=True.
			cluster.Spec.Paused = new(true)
			Expect(k8sClient.Update(ctx, cluster)).To(Succeed())
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(nodePool),
			})
			Expect(err).NotTo(HaveOccurred())

			got := &infrav1.StackitManagedNodePool{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodePool), got)).To(Succeed())
			expectCondition(got.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason)

			// Mark parent StackitManagedCluster with finalizer and delete both parent and child while still paused.
			controllerutil.AddFinalizer(managedCluster, infrav1.ManagedClusterFinalizer)
			Expect(k8sClient.Update(ctx, managedCluster)).To(Succeed())
			Expect(k8sClient.Delete(ctx, managedCluster)).To(Succeed())
			Expect(k8sClient.Delete(ctx, got)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(nodePool),
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodePool), got))).To(BeTrue())
		})

		It("retains ManagedNodePoolFinalizer during standalone deletion until parent projects Reason=Deleted", func() {
			suffix := fmt.Sprintf("%d", time.Now().UnixNano())
			_, _, _, nodePool := createManagedClusterChain(
				"del-cl-"+suffix,
				"del-mc-"+suffix,
				"del-mp-"+suffix,
				"del-pool-"+suffix,
			)

			reconciler := &StackitManagedNodePoolReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(nodePool),
			})
			Expect(err).NotTo(HaveOccurred())

			// Delete only the child StackitManagedNodePool while parent cluster stays active.
			Expect(k8sClient.Delete(ctx, nodePool)).To(Succeed())

			// 1. Before parent projects Reason="Deleted", finalizer is retained.
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(nodePool),
			})
			Expect(err).NotTo(HaveOccurred())

			got := &infrav1.StackitManagedNodePool{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodePool), got)).To(Succeed())
			Expect(got.Finalizers).To(ContainElement(infrav1.ManagedNodePoolFinalizer))

			// 2. Simulate parent projecting Ready=False with Reason="Deleted".
			util.SetConditions(&got.Status.Conditions, got.Generation, metav1.ConditionFalse, "Deleted", "SKE node pool has been deleted", infrav1.ManagedNodePoolReadyCondition)
			Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(nodePool),
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodePool), got))).To(BeTrue())
		})
	})

	Context("Watch predicates and cross-cluster isolation", func() {
		It("ignores status-only updates on StackitManagedNodePool while enqueuing spec, deletion, and finalizer transitions", func() {
			base := &infrav1.StackitManagedNodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "pool-pred",
					Namespace:  namespace,
					Generation: 1,
					Finalizers: []string{infrav1.ManagedNodePoolFinalizer},
					Labels:     map[string]string{clusterv1.ClusterNameLabel: "c1"},
				},
				Spec: validManagedNodePoolSpec(),
			}

			// Status-only update must NOT trigger parent reconcile.
			statusUpdated := base.DeepCopy()
			statusUpdated.Status.Ready = true
			statusUpdated.Status.Initialization.Provisioned = new(true)
			util.SetConditions(&statusUpdated.Status.Conditions, 1, metav1.ConditionTrue, "Available", "", infrav1.ManagedNodePoolReadyCondition)
			Expect(managedNodePoolChanged(event.UpdateEvent{ObjectOld: base, ObjectNew: statusUpdated})).To(BeFalse())

			// Generation (spec) change MUST trigger parent reconcile.
			specUpdated := base.DeepCopy()
			specUpdated.Generation = 2
			Expect(managedNodePoolChanged(event.UpdateEvent{ObjectOld: base, ObjectNew: specUpdated})).To(BeTrue())

			// DeletionTimestamp transition MUST trigger parent reconcile.
			now := metav1.Now()
			deletingUpdated := base.DeepCopy()
			deletingUpdated.DeletionTimestamp = &now
			Expect(managedNodePoolChanged(event.UpdateEvent{ObjectOld: base, ObjectNew: deletingUpdated})).To(BeTrue())

			// Finalizer addition MUST trigger parent reconcile.
			unfinalized := base.DeepCopy()
			unfinalized.Finalizers = nil
			Expect(managedNodePoolChanged(event.UpdateEvent{ObjectOld: unfinalized, ObjectNew: base})).To(BeTrue())
		})

		It("does not aggregate MachinePools whose OwnerReference points to a different Cluster UID", func() {
			suffix := fmt.Sprintf("%d", time.Now().UnixNano())
			cluster, managedCluster, machinePool, _ := createManagedClusterChain(
				"uid-cl-"+suffix,
				"uid-mc-"+suffix,
				"uid-mp-"+suffix,
				"uid-np-"+suffix,
			)

			// Point machinePool's Cluster OwnerReference to a stale Cluster UID.
			machinePool.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Cluster",
				Name:       cluster.Name,
				UID:        types.UID("different-cluster-uid"),
			}}
			Expect(k8sClient.Update(ctx, machinePool)).To(Succeed())

			pools, err := listManagedNodePoolsForCluster(ctx, k8sClient, cluster, managedCluster)
			Expect(err).NotTo(HaveOccurred())
			Expect(pools).To(BeEmpty())
		})
	})

	Context("When mapping MachinePool and StackitManagedNodePool events", func() {
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

		It("enqueues the owning StackitManagedCluster via MachinePool and Cluster", func() {
			suffix := fmt.Sprintf("%d", time.Now().UnixNano())
			_, managedCluster, _, nodePool := createManagedClusterChain(
				"map-cl-"+suffix,
				"map-mc-"+suffix,
				"map-mp-"+suffix,
				"map-np-"+suffix,
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
	})
})
