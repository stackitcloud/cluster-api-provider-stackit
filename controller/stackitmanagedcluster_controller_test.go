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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stackitcloud/stackit-sdk-go/core/oapierror"
	skev2 "github.com/stackitcloud/stackit-sdk-go/services/ske/v2api"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud"
	skeservice "github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/ske"
	"github.com/stackitcloud/cluster-api-provider-stackit/util"
)

type fakeControllerSKEAPI struct {
	skev2.DefaultAPI

	getClusterFn     func() (*skev2.Cluster, error)
	createOrUpdateFn func() (*skev2.Cluster, error)
	deleteClusterFn  func() (map[string]any, error)

	getCalls    int
	createCalls int
	deleteCalls int
}

func (f *fakeControllerSKEAPI) GetCluster(_ context.Context, _, _, _ string) skev2.ApiGetClusterRequest {
	return skev2.ApiGetClusterRequest{ApiService: f}
}

func (f *fakeControllerSKEAPI) GetClusterExecute(_ skev2.ApiGetClusterRequest) (*skev2.Cluster, error) {
	f.getCalls++
	if f.getClusterFn != nil {
		return f.getClusterFn()
	}
	return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusNotFound, ErrorMessage: "404 Not Found"}
}

func (f *fakeControllerSKEAPI) CreateOrUpdateCluster(_ context.Context, _, _, _ string) skev2.ApiCreateOrUpdateClusterRequest {
	return skev2.ApiCreateOrUpdateClusterRequest{ApiService: f}
}

func (f *fakeControllerSKEAPI) CreateOrUpdateClusterExecute(_ skev2.ApiCreateOrUpdateClusterRequest) (*skev2.Cluster, error) {
	f.createCalls++
	if f.createOrUpdateFn != nil {
		return f.createOrUpdateFn()
	}
	return &skev2.Cluster{Name: new("ske-test")}, nil
}

func (f *fakeControllerSKEAPI) DeleteCluster(_ context.Context, _, _, _ string) skev2.ApiDeleteClusterRequest {
	return skev2.ApiDeleteClusterRequest{ApiService: f}
}

func (f *fakeControllerSKEAPI) DeleteClusterExecute(_ skev2.ApiDeleteClusterRequest) (map[string]any, error) {
	f.deleteCalls++
	if f.deleteClusterFn != nil {
		return f.deleteClusterFn()
	}
	return map[string]any{}, nil
}

type failingPatchClient struct {
	client.Client
	failPatch           bool
	failStatusPatch     bool
	failStatusForObject string
}

func (c *failingPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if c.failPatch {
		return errors.New("simulated metadata patch failure")
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *failingPatchClient) Status() client.SubResourceWriter {
	return &failingStatusWriter{
		SubResourceWriter:   c.Client.Status(),
		failStatusPatch:     c.failStatusPatch,
		failStatusForObject: c.failStatusForObject,
	}
}

type failingStatusWriter struct {
	client.SubResourceWriter
	failStatusPatch     bool
	failStatusForObject string
}

func (w *failingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if w.failStatusPatch || (w.failStatusForObject != "" && obj.GetName() == w.failStatusForObject) {
		return errors.New("simulated status patch failure")
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

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

func skeClusterWithState(
	managedCluster *infrav1.StackitManagedCluster,
	state skev2.ClusterStatusState,
	pools ...skev2.Nodepool,
) *skev2.Cluster {
	labels := util.ManagedClusterTags(managedCluster.Name, managedCluster.Namespace, string(managedCluster.UID), nil)
	if len(pools) == 0 {
		pools = []skev2.Nodepool{
			skeservice.ManagedNodePoolDesiredState{
				Name:                  infrav1.ManagedNodePoolBootstrapName,
				AvailabilityZones:     []string{"eu01-1"},
				MachineType:           "c2i.2",
				ImageName:             "flatcar",
				ImageVersion:          "4593.2.5-containerd2.1.9",
				Minimum:               1,
				Maximum:               1,
				VolumeSize:            20,
				VolumeType:            "storage_premium_perf1",
				AllowSystemComponents: new(true),
			}.ToSKESDK(),
		}
	}
	return &skev2.Cluster{
		Name:      new("ske-test"),
		Labels:    &labels,
		Nodepools: pools,
		Status: &skev2.ClusterStatus{
			Aggregated: &state,
		},
	}
}

var _ = Describe("StackitManagedCluster Controller", func() {
	const namespace = "default"

	Context("When reconciling StackitManagedCluster lifecycle", func() {
		var (
			clusterName    string
			credentials    string
			managedKey     types.NamespacedName
			request        reconcile.Request
			ownerCluster   *clusterv1.Cluster
			managedCluster *infrav1.StackitManagedCluster
			fakeSKE        *fakeControllerSKEAPI
			reconciler     *StackitManagedClusterReconciler
		)

		BeforeEach(func() {
			clusterName = fmt.Sprintf("mc-%d", time.Now().UnixNano())
			credentials = "stackit-creds-" + clusterName
			managedKey = types.NamespacedName{Namespace: namespace, Name: clusterName}
			request = reconcile.Request{NamespacedName: managedKey}
			fakeSKE = &fakeControllerSKEAPI{}

			createCredentialsSecret(ctx, credentials, namespace, testProjectID)

			ownerCluster = &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: clusterv1.ClusterSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infrav1.GroupVersion.Group,
						Kind:     "StackitManagedCluster",
						Name:     clusterName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, ownerCluster)).To(Succeed())

			spec := validManagedClusterSpec()
			spec.CredentialsSecretRef.Name = credentials
			spec.CredentialsSecretRef.Namespace = namespace
			managedCluster = &infrav1.StackitManagedCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: clusterv1.GroupVersion.String(),
						Kind:       "Cluster",
						Name:       ownerCluster.Name,
						UID:        ownerCluster.UID,
					}},
				},
				Spec: spec,
			}
			Expect(k8sClient.Create(ctx, managedCluster)).To(Succeed())

			reconciler = &StackitManagedClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				SKEClientFactory: func(context.Context, cloud.Credentials) (skev2.DefaultAPI, error) {
					return fakeSKE, nil
				},
			}
		})

		AfterEach(func() {
			got := &infrav1.StackitManagedCluster{}
			if err := k8sClient.Get(ctx, managedKey, got); err == nil {
				got.Finalizers = nil
				_ = k8sClient.Update(ctx, got)
				deleteIfExists(ctx, got)
			}
			deleteIfExists(ctx, ownerCluster)
			deleteIfExists(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: credentials, Namespace: namespace}})
		})

		It("returns nil when the resource is not found", func() {
			res, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: namespace, Name: "non-existent"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))
		})

		It("persists the finalizer before calling SKE CreateOrUpdateCluster and transitions Ready and Provisioned correctly", func() {
			finalizerPresentDuringCreate := false
			fakeSKE.createOrUpdateFn = func() (*skev2.Cluster, error) {
				persisted := &infrav1.StackitManagedCluster{}
				Expect(k8sClient.Get(ctx, managedKey, persisted)).To(Succeed())
				for _, f := range persisted.Finalizers {
					if f == infrav1.ManagedClusterFinalizer {
						finalizerPresentDuringCreate = true
					}
				}
				return &skev2.Cluster{Name: new("ske-test")}, nil
			}

			// 1. Initial reconcile: cluster is 404 -> finalizer persisted first, then CreateOrUpdateCluster called.
			res, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(finalizerPresentDuringCreate).To(BeTrue(), "finalizer must be persisted in API server before CreateOrUpdateCluster is called")
			Expect(fakeSKE.createCalls).To(Equal(1))

			got := &infrav1.StackitManagedCluster{}
			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			Expect(got.Finalizers).To(ContainElement(infrav1.ManagedClusterFinalizer))
			Expect(got.Status.Ready).To(BeFalse())
			Expect(got.Status.Initialization.Provisioned).To(BeNil())
			expectCondition(got.Status.Conditions, infrav1.ManagedClusterReadyCondition, metav1.ConditionFalse, "Creating")

			// 2. Second reconcile: SKE is STATE_CREATING -> polls without calling CreateOrUpdateCluster again.
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_CREATING), nil
			}
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(fakeSKE.createCalls).To(Equal(1))

			// 3. Third reconcile: SKE reaches STATE_HEALTHY -> Ready=true and Initialization.Provisioned=true.
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY), nil
			}
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))

			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			Expect(got.Status.Ready).To(BeTrue())
			Expect(got.Status.Initialization.Provisioned).NotTo(BeNil())
			Expect(*got.Status.Initialization.Provisioned).To(BeTrue())
			expectCondition(got.Status.Conditions, infrav1.ManagedClusterReadyCondition, metav1.ConditionTrue, "Available")

			// 4. Fourth reconcile: SKE becomes STATE_UNHEALTHY -> Ready=false, but Provisioned stays true (sticky).
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				c := skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_UNHEALTHY)
				c.Status.Errors = []skev2.ClusterError{{Message: new("node pressure")}}
				return c, nil
			}
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))

			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			Expect(got.Status.Ready).To(BeFalse())
			Expect(got.Status.Initialization.Provisioned).NotTo(BeNil())
			Expect(*got.Status.Initialization.Provisioned).To(BeTrue())
			expectCondition(got.Status.Conditions, infrav1.ManagedClusterReadyCondition, metav1.ConditionFalse, "Unhealthy")

			// 5. Fifth reconcile: SKE reports an unknown state -> Ready=false (UnknownState), Provisioned stays true.
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_UNKNOWN_DEFAULT_OPEN_API), nil
			}
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))

			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			Expect(got.Status.Ready).To(BeFalse())
			Expect(got.Status.Initialization.Provisioned).NotTo(BeNil())
			Expect(*got.Status.Initialization.Provisioned).To(BeTrue())
			expectCondition(got.Status.Conditions, infrav1.ManagedClusterReadyCondition, metav1.ConditionFalse, "UnknownState")
		})

		It("does not call CreateOrUpdateCluster if persisting the finalizer fails", func() {
			reconciler.Client = &failingPatchClient{
				Client:    k8sClient,
				failPatch: true,
			}

			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated metadata patch failure"))
			Expect(fakeSKE.createCalls).To(Equal(0))
		})

		It("propagates status patch errors from deferred PatchObject", func() {
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY), nil
			}
			reconciler.Client = &failingPatchClient{
				Client:          k8sClient,
				failStatusPatch: true,
			}

			res, err := reconciler.Reconcile(ctx, request)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated status patch failure"))
			Expect(res).To(Equal(reconcile.Result{}))
		})

		It("keeps the finalizer during asynchronous deletion and removes it only after confirmed HTTP 404", func() {
			// Provision first so finalizer is present.
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY), nil
			}
			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Delete(ctx, managedCluster)).To(Succeed())

			// 1. Delete triggered while cluster is HEALTHY -> calls DeleteCluster, retains finalizer.
			res, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(fakeSKE.deleteCalls).To(Equal(1))

			got := &infrav1.StackitManagedCluster{}
			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			Expect(got.Finalizers).To(ContainElement(infrav1.ManagedClusterFinalizer))
			expectCondition(got.Status.Conditions, infrav1.ManagedClusterReadyCondition, metav1.ConditionFalse, "Deleting")

			// 2. Next poll while SKE is STATE_DELETING -> does not call DeleteCluster again, retains finalizer.
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_DELETING), nil
			}
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(fakeSKE.deleteCalls).To(Equal(1))
			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			Expect(got.Finalizers).To(ContainElement(infrav1.ManagedClusterFinalizer))

			// 3. Next poll returns HTTP 404 NotFound -> removes finalizer and object is deleted.
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusNotFound, ErrorMessage: "404 Not Found"}
			}
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))

			err = k8sClient.Get(ctx, managedKey, got)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})

		It("completes deletion even when the owning Cluster is already deleted and the resource is paused", func() {
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY), nil
			}
			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())

			// Delete the owning CAPI Cluster first.
			Expect(k8sClient.Delete(ctx, ownerCluster)).To(Succeed())

			// Mark StackitManagedCluster as paused and delete it.
			got := &infrav1.StackitManagedCluster{}
			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			if got.Annotations == nil {
				got.Annotations = map[string]string{}
			}
			got.Annotations[clusterv1.PausedAnnotation] = "true"
			Expect(k8sClient.Update(ctx, got)).To(Succeed())
			Expect(k8sClient.Delete(ctx, got)).To(Succeed())

			// First delete reconcile triggers DeleteCluster despite missing owner Cluster and paused annotation.
			res, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(fakeSKE.deleteCalls).To(Equal(1))

			// Second delete reconcile observes 404 and removes the finalizer.
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusNotFound, ErrorMessage: "404 Not Found"}
			}
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, managedKey, got))).To(BeTrue())
		})

		It("retains the finalizer when credentials Secret is missing during deletion", func() {
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY), nil
			}
			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())

			// Delete the credentials Secret and then delete StackitManagedCluster.
			Expect(k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: credentials, Namespace: namespace},
			})).To(Succeed())
			Expect(k8sClient.Delete(ctx, managedCluster)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).To(HaveOccurred())
			Expect(fakeSKE.deleteCalls).To(Equal(0))

			got := &infrav1.StackitManagedCluster{}
			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			Expect(got.Finalizers).To(ContainElement(infrav1.ManagedClusterFinalizer))
			expectCondition(got.Status.Conditions, infrav1.ManagedClusterReadyCondition, metav1.ConditionFalse, "CredentialsError")
		})

		It("refuses to adopt or delete a pre-existing SKE cluster with the same name and namespace but a different UID", func() {
			state := skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY
			staleLabels := util.ManagedClusterTags(managedCluster.Name, managedCluster.Namespace, "stale-uid-from-previous-cr", nil)
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return &skev2.Cluster{
					Name:   new("ske-test"),
					Labels: &staleLabels,
					Status: &skev2.ClusterStatus{
						Aggregated: &state,
					},
				}, nil
			}

			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).To(HaveOccurred())
			Expect(fakeSKE.createCalls).To(Equal(0))

			got := &infrav1.StackitManagedCluster{}
			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			Expect(got.Status.Ready).To(BeFalse())
			Expect(got.Status.Initialization.Provisioned).To(BeNil())
			expectCondition(got.Status.Conditions, infrav1.ManagedClusterReadyCondition, metav1.ConditionFalse, "ClusterNotManaged")

			// Deleting the StackitManagedCluster must NOT delete the unrelated SKE cluster.
			Expect(k8sClient.Delete(ctx, managedCluster)).To(Succeed())
			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).To(HaveOccurred())
			Expect(fakeSKE.deleteCalls).To(Equal(0))
			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			Expect(got.Finalizers).To(ContainElement(infrav1.ManagedClusterFinalizer))
			expectCondition(got.Status.Conditions, infrav1.ManagedClusterReadyCondition, metav1.ConditionFalse, "ClusterNotManaged")
		})

		It("tolerates and removes the legacy ManagedCluster finalizer during deletion", func() {
			got := &infrav1.StackitManagedCluster{}
			Expect(k8sClient.Get(ctx, managedKey, got)).To(Succeed())
			got.Finalizers = []string{infrav1.LegacyManagedClusterFinalizer}
			Expect(k8sClient.Update(ctx, got)).To(Succeed())
			Expect(k8sClient.Delete(ctx, got)).To(Succeed())

			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusNotFound, ErrorMessage: "404 Not Found"}
			}
			res, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, managedKey, got))).To(BeTrue())
		})

		It("aggregates multiple StackitManagedNodePools with a single GetCluster, projects child statuses, and completes standalone and full-cluster deletion", func() {
			npReconciler := &StackitManagedNodePoolReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			createChildPool := func(suffix, skePoolName string) *infrav1.StackitManagedNodePool {
				mpName := fmt.Sprintf("%s-mp-%s", clusterName, suffix)
				npName := fmt.Sprintf("%s-np-%s", clusterName, suffix)
				mp := &clusterv1.MachinePool{
					ObjectMeta: metav1.ObjectMeta{
						Name:      mpName,
						Namespace: namespace,
						Labels:    map[string]string{clusterv1.ClusterNameLabel: clusterName},
						OwnerReferences: []metav1.OwnerReference{{
							APIVersion: clusterv1.GroupVersion.String(),
							Kind:       "Cluster",
							Name:       ownerCluster.Name,
							UID:        ownerCluster.UID,
						}},
					},
					Spec: clusterv1.MachinePoolSpec{
						ClusterName: clusterName,
						Template: clusterv1.MachineTemplateSpec{
							Spec: clusterv1.MachineSpec{
								ClusterName: clusterName,
								Bootstrap:   clusterv1.Bootstrap{DataSecretName: new("")},
								InfrastructureRef: clusterv1.ContractVersionedObjectReference{
									APIGroup: infrav1.GroupVersion.Group,
									Kind:     "StackitManagedNodePool",
									Name:     npName,
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, mp)).To(Succeed())
				DeferCleanup(func() { deleteIfExists(ctx, mp) })

				spec := validManagedNodePoolSpec()
				spec.Name = skePoolName
				np := &infrav1.StackitManagedNodePool{
					ObjectMeta: metav1.ObjectMeta{
						Name:      npName,
						Namespace: namespace,
						Labels:    map[string]string{clusterv1.ClusterNameLabel: clusterName},
						OwnerReferences: []metav1.OwnerReference{{
							APIVersion: clusterv1.GroupVersion.String(),
							Kind:       "MachinePool",
							Name:       mp.Name,
							UID:        mp.UID,
						}},
					},
					Spec: spec,
				}
				Expect(k8sClient.Create(ctx, np)).To(Succeed())
				DeferCleanup(func() {
					got := &infrav1.StackitManagedNodePool{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(np), got); err == nil {
						got.Finalizers = nil
						_ = k8sClient.Update(ctx, got)
						deleteIfExists(ctx, got)
					}
				})

				_, err := npReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(np)})
				Expect(err).NotTo(HaveOccurred())
				return np
			}

			poolA := createChildPool("a", "pool-a")
			poolB := createChildPool("b", "pool-b")

			// Verify child reconciler made 0 SKE calls.
			Expect(fakeSKE.getCalls).To(Equal(0))
			Expect(fakeSKE.createCalls).To(Equal(0))

			sdkPoolA := skeservice.NodePoolDesiredState(poolA).ToSKESDK()
			sdkPoolB := skeservice.NodePoolDesiredState(poolB).ToSKESDK()
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, sdkPoolA, sdkPoolB), nil
			}

			// Simulate a partial child status-patch failure on poolB first.
			reconciler.Client = &failingPatchClient{
				Client:              k8sClient,
				failStatusForObject: poolB.Name,
			}
			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated status patch failure"))
			reconciler.Client = k8sClient

			// Next reconcile succeeds and projects status onto both children using 1 GetCluster call.
			beforeGets := fakeSKE.getCalls
			res, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))
			Expect(fakeSKE.getCalls - beforeGets).To(Equal(1))

			gotA := &infrav1.StackitManagedNodePool{}
			gotB := &infrav1.StackitManagedNodePool{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolA), gotA)).To(Succeed())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolB), gotB)).To(Succeed())
			Expect(gotA.Status.Ready).To(BeTrue())
			Expect(*gotA.Status.Initialization.Provisioned).To(BeTrue())
			Expect(gotA.Status.Replicas).To(BeZero())
			Expect(gotA.Spec.ProviderIDList).To(BeEmpty())
			expectCondition(gotA.Status.Conditions, infrav1.ManagedNodePoolReadyCondition, metav1.ConditionTrue, "Available")
			expectCondition(gotA.Status.Conditions, clusterv1.PausedCondition, metav1.ConditionFalse, clusterv1.NotPausedReason)
			Expect(gotB.Status.Ready).To(BeTrue())
			Expect(*gotB.Status.Initialization.Provisioned).To(BeTrue())

			// Standalone deletion of poolB while poolA remains active:
			Expect(k8sClient.Delete(ctx, poolB)).To(Succeed())
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(fakeSKE.createCalls).To(Equal(1))

			// Next poll confirms poolB is absent from SKE -> parent writes Reason="Deleted" on poolB.
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, sdkPoolA), nil
			}
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolB), gotB)).To(Succeed())
			expectCondition(gotB.Status.Conditions, infrav1.ManagedNodePoolReadyCondition, metav1.ConditionFalse, "Deleted")

			// Child reconciler observes Reason="Deleted" and removes its finalizer with 0 SKE calls.
			beforeGets = fakeSKE.getCalls
			_, err = npReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(poolB)})
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeSKE.getCalls).To(Equal(beforeGets))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolB), gotB))).To(BeTrue())

			// Full cluster teardown with poolA still present:
			Expect(k8sClient.Delete(ctx, managedCluster)).To(Succeed())
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(managedClusterDeleteRequeueAfter))
			// Must NOT call DeleteCluster yet while poolA still exists, and must NOT issue extra PUTs.
			Expect(fakeSKE.deleteCalls).To(Equal(0))
			Expect(fakeSKE.createCalls).To(Equal(1))

			// poolA hits parent-deleting fast-path and releases its finalizer immediately.
			_, err = npReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(poolA)})
			Expect(err).NotTo(HaveOccurred())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolA), gotA))).To(BeTrue())

			// Now StackitManagedCluster proceeds with SKE DeleteCluster and removes its finalizer on 404.
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(fakeSKE.deleteCalls).To(Equal(1))

			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return nil, &oapierror.GenericOpenAPIError{StatusCode: http.StatusNotFound, ErrorMessage: "404 Not Found"}
			}
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, managedKey, &infrav1.StackitManagedCluster{}))).To(BeTrue())
		})

		It("resolves duplicate node-pool name deadlock when one of the colliding StackitManagedNodePools is deleted", func() {
			npReconciler := &StackitManagedNodePoolReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			createDupPool := func(suffix string) *infrav1.StackitManagedNodePool {
				mpName := fmt.Sprintf("%s-dup-mp-%s", clusterName, suffix)
				npName := fmt.Sprintf("%s-dup-np-%s", clusterName, suffix)
				mp := &clusterv1.MachinePool{
					ObjectMeta: metav1.ObjectMeta{
						Name:      mpName,
						Namespace: namespace,
						Labels:    map[string]string{clusterv1.ClusterNameLabel: clusterName},
						OwnerReferences: []metav1.OwnerReference{{
							APIVersion: clusterv1.GroupVersion.String(),
							Kind:       "Cluster",
							Name:       ownerCluster.Name,
							UID:        ownerCluster.UID,
						}},
					},
					Spec: clusterv1.MachinePoolSpec{
						ClusterName: clusterName,
						Template: clusterv1.MachineTemplateSpec{
							Spec: clusterv1.MachineSpec{
								ClusterName: clusterName,
								Bootstrap:   clusterv1.Bootstrap{DataSecretName: new("")},
								InfrastructureRef: clusterv1.ContractVersionedObjectReference{
									APIGroup: infrav1.GroupVersion.Group,
									Kind:     "StackitManagedNodePool",
									Name:     npName,
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, mp)).To(Succeed())
				DeferCleanup(func() { deleteIfExists(ctx, mp) })

				spec := validManagedNodePoolSpec()
				spec.Name = "workers"
				np := &infrav1.StackitManagedNodePool{
					ObjectMeta: metav1.ObjectMeta{
						Name:      npName,
						Namespace: namespace,
						Labels:    map[string]string{clusterv1.ClusterNameLabel: clusterName},
						OwnerReferences: []metav1.OwnerReference{{
							APIVersion: clusterv1.GroupVersion.String(),
							Kind:       "MachinePool",
							Name:       mp.Name,
							UID:        mp.UID,
						}},
					},
					Spec: spec,
				}
				Expect(k8sClient.Create(ctx, np)).To(Succeed())
				DeferCleanup(func() {
					got := &infrav1.StackitManagedNodePool{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(np), got); err == nil {
						got.Finalizers = nil
						_ = k8sClient.Update(ctx, got)
						deleteIfExists(ctx, got)
					}
				})

				_, err := npReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(np)})
				Expect(err).NotTo(HaveOccurred())
				return np
			}

			poolA := createDupPool("a")
			poolB := createDupPool("b")

			sdkWorkers := skeservice.NodePoolDesiredState(poolA).ToSKESDK()
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, sdkWorkers), nil
			}

			// 1. Both pools active with spec.name="workers" -> DuplicateNodePoolName, 0 SKE calls.
			res, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))
			Expect(fakeSKE.getCalls).To(Equal(0))

			gotA := &infrav1.StackitManagedNodePool{}
			gotB := &infrav1.StackitManagedNodePool{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolA), gotA)).To(Succeed())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolB), gotB)).To(Succeed())
			expectCondition(gotA.Status.Conditions, infrav1.ManagedNodePoolReadyCondition, metav1.ConditionFalse, skeservice.ReasonDuplicateNodePoolName)
			expectCondition(gotB.Status.Conditions, infrav1.ManagedNodePoolReadyCondition, metav1.ConditionFalse, skeservice.ReasonDuplicateNodePoolName)

			// 2. Delete poolB to resolve the duplicate conflict.
			Expect(k8sClient.Delete(ctx, poolB)).To(Succeed())
			res, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(reconcile.Result{}))
			Expect(fakeSKE.createCalls).To(Equal(0))

			// poolB gets Reason=Deleted while poolA becomes Ready=true.
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolA), gotA)).To(Succeed())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolB), gotB)).To(Succeed())
			Expect(gotA.Status.Ready).To(BeTrue())
			expectCondition(gotB.Status.Conditions, infrav1.ManagedNodePoolReadyCondition, metav1.ConditionFalse, skeservice.ReasonDeleted)

			// 3. Reconciling poolB removes its finalizer and deletes the CR.
			_, err = npReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(poolB)})
			Expect(err).NotTo(HaveOccurred())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(poolB), gotB))).To(BeTrue())
		})

		It("enforces per-pool pause semantics for both StackitManagedNodePool and MachinePool pause annotations", func() {
			npReconciler := &StackitManagedNodePoolReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			mp := &clusterv1.MachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName + "-pause-mp",
					Namespace: namespace,
					Labels:    map[string]string{clusterv1.ClusterNameLabel: clusterName},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: clusterv1.GroupVersion.String(),
						Kind:       "Cluster",
						Name:       ownerCluster.Name,
						UID:        ownerCluster.UID,
					}},
				},
				Spec: clusterv1.MachinePoolSpec{
					ClusterName: clusterName,
					Template: clusterv1.MachineTemplateSpec{
						Spec: clusterv1.MachineSpec{
							ClusterName: clusterName,
							Bootstrap:   clusterv1.Bootstrap{DataSecretName: new("")},
							InfrastructureRef: clusterv1.ContractVersionedObjectReference{
								APIGroup: infrav1.GroupVersion.Group,
								Kind:     "StackitManagedNodePool",
								Name:     clusterName + "-pause-np",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, mp)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, mp) })

			np := &infrav1.StackitManagedNodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName + "-pause-np",
					Namespace: namespace,
					Labels:    map[string]string{clusterv1.ClusterNameLabel: clusterName},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: clusterv1.GroupVersion.String(),
						Kind:       "MachinePool",
						Name:       mp.Name,
						UID:        mp.UID,
					}},
				},
				Spec: validManagedNodePoolSpec(),
			}
			Expect(k8sClient.Create(ctx, np)).To(Succeed())
			DeferCleanup(func() {
				got := &infrav1.StackitManagedNodePool{}
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(np), got); err == nil {
					got.Finalizers = nil
					_ = k8sClient.Update(ctx, got)
					deleteIfExists(ctx, got)
				}
			})

			// Finalize child and reconcile healthy cluster.
			_, err := npReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(np)})
			Expect(err).NotTo(HaveOccurred())

			initialSDKPool := skeservice.NodePoolDesiredState(np).ToSKESDK()
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY, initialSDKPool), nil
			}
			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeSKE.createCalls).To(Equal(0))

			// 1. Pause StackitManagedNodePool and change spec.maximum -> 0 SKE PUT calls.
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)).To(Succeed())
			np.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}
			np.Spec.Maximum = 5
			Expect(k8sClient.Update(ctx, np)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeSKE.createCalls).To(Equal(0))

			// 2. Unpause StackitManagedNodePool, but pause MachinePool -> still 0 SKE PUT calls.
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mp), mp)).To(Succeed())
			mp.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}
			Expect(k8sClient.Update(ctx, mp)).To(Succeed())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)).To(Succeed())
			delete(np.Annotations, clusterv1.PausedAnnotation)
			Expect(k8sClient.Update(ctx, np)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeSKE.createCalls).To(Equal(0))

			// 3. Unpause MachinePool -> desired spec change (Maximum=5) is now pushed to SKE.
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mp), mp)).To(Succeed())
			delete(mp.Annotations, clusterv1.PausedAnnotation)
			Expect(k8sClient.Update(ctx, mp)).To(Succeed())

			res, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(fakeSKE.createCalls).To(Equal(1))

			// 4. Pause again and delete parent StackitManagedCluster -> parent deletion bypasses pause.
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), np)).To(Succeed())
			np.Annotations = map[string]string{clusterv1.PausedAnnotation: "true"}
			Expect(k8sClient.Update(ctx, np)).To(Succeed())
			Expect(k8sClient.Delete(ctx, managedCluster)).To(Succeed())

			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			_, err = npReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(np)})
			Expect(err).NotTo(HaveOccurred())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(np), &infrav1.StackitManagedNodePool{}))).To(BeTrue())
		})

		It("does not select or delete a StackitManagedNodePool belonging to a recreated Cluster with the same name but a different UID", func() {
			fakeSKE.getClusterFn = func() (*skev2.Cluster, error) {
				return skeClusterWithState(managedCluster, skev2.CLUSTERSTATUSSTATE_STATE_HEALTHY), nil
			}
			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())

			// Delete the original Cluster (UID=A) and mark old StackitManagedCluster deleting.
			Expect(k8sClient.Delete(ctx, ownerCluster)).To(Succeed())
			Expect(k8sClient.Delete(ctx, managedCluster)).To(Succeed())

			// Recreate a new Cluster with the exact same name (which gets a new UID=B) and a new MachinePool + StackitManagedNodePool.
			recreatedCluster := &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
				},
				Spec: clusterv1.ClusterSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infrav1.GroupVersion.Group,
						Kind:     "StackitManagedCluster",
						Name:     clusterName + "-v2",
					},
				},
			}
			Expect(k8sClient.Create(ctx, recreatedCluster)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, recreatedCluster) })

			newMP := &clusterv1.MachinePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName + "-new-mp",
					Namespace: namespace,
					Labels:    map[string]string{clusterv1.ClusterNameLabel: clusterName},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: clusterv1.GroupVersion.String(),
						Kind:       "Cluster",
						Name:       recreatedCluster.Name,
						UID:        recreatedCluster.UID,
					}},
				},
				Spec: clusterv1.MachinePoolSpec{
					ClusterName: clusterName,
					Template: clusterv1.MachineTemplateSpec{
						Spec: clusterv1.MachineSpec{
							ClusterName: clusterName,
							Bootstrap:   clusterv1.Bootstrap{DataSecretName: new("")},
							InfrastructureRef: clusterv1.ContractVersionedObjectReference{
								APIGroup: infrav1.GroupVersion.Group,
								Kind:     "StackitManagedNodePool",
								Name:     clusterName + "-new-np",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, newMP)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, newMP) })

			newNP := &infrav1.StackitManagedNodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName + "-new-np",
					Namespace: namespace,
					Labels: map[string]string{
						clusterv1.ClusterNameLabel: clusterName,
						util.LabelClusterUID:       "new-managed-cluster-uid",
					},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: clusterv1.GroupVersion.String(),
						Kind:       "MachinePool",
						Name:       newMP.Name,
						UID:        newMP.UID,
					}},
				},
				Spec: validManagedNodePoolSpec(),
			}
			Expect(k8sClient.Create(ctx, newNP)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, newNP) })

			// Reconciling the old terminating StackitManagedCluster must NOT delete or wait on newNP;
			// it proceeds directly to SKE DeleteCluster.
			res, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(time.Minute))
			Expect(fakeSKE.deleteCalls).To(Equal(1))

			gotNewNP := &infrav1.StackitManagedNodePool{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newNP), gotNewNP)).To(Succeed())
			Expect(gotNewNP.DeletionTimestamp.IsZero()).To(BeTrue())
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
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects an invalid region", func() {
			cluster := newManagedCluster("invalid-region-mc")
			cluster.Spec.Region = "eu-central-1"
			err := k8sClient.Create(ctx, cluster)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects an invalid network ID when network is specified", func() {
			cluster := newManagedCluster("invalid-network-mc")
			cluster.Spec.Network = &infrav1.StackitManagedClusterNetworkSpec{
				ID: "invalid-network-id",
			}
			err := k8sClient.Create(ctx, cluster)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects missing credentials secret name", func() {
			cluster := newManagedCluster("missing-creds-mc")
			cluster.Spec.CredentialsSecretRef.Name = ""
			err := k8sClient.Create(ctx, cluster)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
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
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects an uppercase cluster name", func() {
			cluster := newManagedCluster("uppercase-name-mc")
			cluster.Spec.ClusterName = "SkeCluster"
			err := k8sClient.Create(ctx, cluster)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects a cluster name containing an underscore", func() {
			cluster := newManagedCluster("underscore-name-mc")
			cluster.Spec.ClusterName = "ske_cluster"
			err := k8sClient.Create(ctx, cluster)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects a cluster name with a leading hyphen", func() {
			cluster := newManagedCluster("leading-hyphen-mc")
			cluster.Spec.ClusterName = "-skecluster"
			err := k8sClient.Create(ctx, cluster)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects a cluster name with a trailing hyphen", func() {
			cluster := newManagedCluster("trailing-hyphen-mc")
			cluster.Spec.ClusterName = "skecluster-"
			err := k8sClient.Create(ctx, cluster)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
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
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects Kubernetes version v1.35.3 with leading v", func() {
			cluster := newManagedCluster("v-prefix-k8s-ver-mc")
			cluster.Spec.Kubernetes.Version = "v1.35.3"
			err := k8sClient.Create(ctx, cluster)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects Kubernetes version latest", func() {
			cluster := newManagedCluster("latest-k8s-ver-mc")
			cluster.Spec.Kubernetes.Version = "latest"
			err := k8sClient.Create(ctx, cluster)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
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
			Expect(apierrors.IsInvalid(k8sClient.Status().Update(ctx, raw))).To(BeTrue())
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
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects the reserved provider bootstrap node-pool name capstk-system", func() {
			pool := newManagedNodePool("reserved-name-np")
			pool.Spec.Name = infrav1.ManagedNodePoolBootstrapName
			err := k8sClient.Create(ctx, pool)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("reserved for the provider bootstrap node pool"))
		})

		It("enforces immutability of spec.name after creation", func() {
			pool := newManagedNodePool("immutable-name-np")
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())
			DeferCleanup(func() { deleteIfExists(ctx, pool) })

			pool.Spec.Name = "renamed-np"
			err := k8sClient.Update(ctx, pool)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("name is immutable"))
		})

		It("rejects missing required availabilityZones", func() {
			pool := newManagedNodePool("missing-azs-np")
			pool.Spec.AvailabilityZones = nil
			err := k8sClient.Create(ctx, pool)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects missing machine", func() {
			raw := newUnstructuredManagedNodePool("missing-machine-np")
			unstructured.RemoveNestedField(raw.Object, "spec", "machine")
			err := k8sClient.Create(ctx, raw)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects missing required machine image fields", func() {
			rawMissingImage := newUnstructuredManagedNodePool("missing-image-np")
			unstructured.RemoveNestedField(rawMissingImage.Object, "spec", "machine", "image")
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, rawMissingImage))).To(BeTrue())

			rawMissingImageName := newUnstructuredManagedNodePool("missing-image-name-np")
			unstructured.RemoveNestedField(rawMissingImageName.Object, "spec", "machine", "image", "name")
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, rawMissingImageName))).To(BeTrue())

			rawMissingImageVersion := newUnstructuredManagedNodePool("missing-image-ver-np")
			unstructured.RemoveNestedField(rawMissingImageVersion.Object, "spec", "machine", "image", "version")
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, rawMissingImageVersion))).To(BeTrue())
		})

		It("rejects missing volume", func() {
			raw := newUnstructuredManagedNodePool("missing-volume-np")
			unstructured.RemoveNestedField(raw.Object, "spec", "volume")
			err := k8sClient.Create(ctx, raw)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects node-pool labels exceeding MaxProperties=80", func() {
			pool := newManagedNodePool("too-many-labels-np")
			pool.Spec.Labels = make(map[string]string, 81)
			for i := range 81 {
				pool.Spec.Labels[fmt.Sprintf("k%d", i)] = "v"
			}
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, pool))).To(BeTrue())
		})

		It("rejects minimum greater than maximum", func() {
			pool := newManagedNodePool("min-gt-max-np")
			pool.Spec.Minimum = 5
			pool.Spec.Maximum = 3
			err := k8sClient.Create(ctx, pool)
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("minimum must be less than or equal to maximum"))
		})

		It("rejects numeric limits outside verified SKE bounds", func() {
			poolMaxZero := newManagedNodePool("max-zero-np")
			poolMaxZero.Spec.Minimum = 0
			poolMaxZero.Spec.Maximum = 0
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, poolMaxZero))).To(BeTrue())

			poolMaxOver1000 := newManagedNodePool("max-over-1000-np")
			poolMaxOver1000.Spec.Maximum = 1001
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, poolMaxOver1000))).To(BeTrue())

			poolMinOver1000 := newManagedNodePool("min-over-1000-np")
			poolMinOver1000.Spec.Minimum = 1001
			poolMinOver1000.Spec.Maximum = 1000
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, poolMinOver1000))).To(BeTrue())

			poolVolTooSmall := newManagedNodePool("vol-small-np")
			poolVolTooSmall.Spec.Volume.Size = 19
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, poolVolTooSmall))).To(BeTrue())

			poolVolTooLarge := newManagedNodePool("vol-large-np")
			poolVolTooLarge.Spec.Volume.Size = 10241
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, poolVolTooLarge))).To(BeTrue())
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
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		})

		It("rejects invalid providerIDList items violating CAPI contract bounds", func() {
			poolEmptyItem := newManagedNodePool("empty-providerid-np")
			poolEmptyItem.Spec.ProviderIDList = []string{""}
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, poolEmptyItem))).To(BeTrue())

			poolTooLongItem := newManagedNodePool("long-providerid-np")
			poolTooLongItem.Spec.ProviderIDList = []string{strings.Repeat("a", 513)}
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, poolTooLongItem))).To(BeTrue())
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
			Expect(apierrors.IsInvalid(k8sClient.Status().Update(ctx, raw))).To(BeTrue())
		})
	})
})
