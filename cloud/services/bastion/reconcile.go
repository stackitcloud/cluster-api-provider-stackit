/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bastion

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/stackitcloud/cluster-api-provider-stackit/api/v1alpha1"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/publicip"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/securitygroup"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/server"
	"github.com/stackitcloud/cluster-api-provider-stackit/cloud/services/shared"
)

func (s *Service) Reconcile(ctx context.Context) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !s.scope.BastionEnabled() {
		// The reason is set only after a cleanup has succeeded, so anything else
		// means bastion resources may still exist. Keying on it rather than on
		// the status keeps the tag-based cleanup to once per cluster.
		condition := s.scope.FindCondition(infrav1.ClusterBastionReadyCondition)
		if condition == nil || condition.Reason != DisabledReason {
			if err := s.deleteResources(ctx); err != nil {
				return s.handleError(ctx, err)
			}
		}
		s.scope.SetConditions(
			metav1.ConditionTrue,
			DisabledReason,
			"bastion disabled",
			infrav1.ClusterBastionReadyCondition,
		)
		return ctrl.Result{}, nil
	}

	cloudInit, err := s.scope.ResolveBastionCloudInit(ctx)
	if err != nil {
		log.Error(err, "Failed to resolve bastion cloud-init", "cluster", s.scope.Name())
		s.scope.Warnf("CloudInitRefError", "Failed to resolve bastion cloudInitRef: %v", err)
		s.scope.SetNotReady(
			"CloudInitRefError",
			err.Error(),
			infrav1.ClusterBastionReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{}, nil
	}
	cloudInitHash := CloudInitHash(cloudInit)
	s.scope.SetBastionCloudInit(cloudInit, cloudInitHash)

	if s.needsRecreate(cloudInitHash) {
		if err := server.New(s.scope).DeletePrimitive(ctx); err != nil {
			return s.handleError(ctx, err)
		}
		s.scope.SetNotReady(
			"Recreating",
			"recreating bastion because cloudInitRef content changed",
			infrav1.ClusterBastionReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		s.scope.Event(
			"BastionRecreating",
			"Recreating bastion because cloudInitRef content changed",
		)
		return ctrl.Result{RequeueAfter: shared.RetryableErrorRequeueAfter}, nil
	}

	srv, created, err := server.New(s.scope).ReconcilePrimitive(ctx)
	if err != nil {
		return s.handleError(ctx, err)
	}
	if created {
		s.scope.Eventf("BastionCreated", "Created bastion %s", srv.ID)
	}
	if err := securitygroup.New(s.scope).ReconcilePrimitive(ctx); err != nil {
		return s.handleError(ctx, err)
	}
	if _, err := publicip.New(s.scope).Reconcile(ctx); err != nil {
		return s.handleError(ctx, err)
	}

	if ready, reason, message, warn := server.StateCondition(srv.State, srv.PowerStatus); !ready {
		previousReason := ""
		if previous := s.scope.FindCondition(infrav1.ClusterBastionReadyCondition); previous != nil {
			previousReason = previous.Reason
		}
		s.scope.SetNotReady(reason, "bastion "+message, infrav1.ClusterBastionReadyCondition, infrav1.ClusterReadyCondition)
		// Only warn on entering the state, not on every requeue.
		if warn && previousReason != reason {
			s.scope.Warnf(reason, "Bastion server %s: %s", srv.ID, message)
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	if s.scope.BastionPublicIP() == "" {
		s.scope.SetNotReady(
			"Provisioning",
			"waiting for bastion public IP address",
			infrav1.ClusterBastionReadyCondition,
			infrav1.ClusterReadyCondition,
		)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	s.scope.SetConditions(
		metav1.ConditionTrue,
		"Available",
		"",
		infrav1.ClusterBastionReadyCondition,
	)
	return ctrl.Result{}, nil
}

func (s *Service) Delete(ctx context.Context) (ctrl.Result, error) {
	if err := s.deleteResources(ctx); err != nil {
		log := logf.FromContext(ctx)
		log.Error(err, "Failed to delete bastion", "cluster", s.scope.Name())
		s.scope.Warnf("FailedDeleteBastion", "Failed to delete bastion: %v", err)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (s *Service) deleteResources(ctx context.Context) error {
	hadBastion := s.scope.HasBastionStatus()
	if _, err := publicip.New(s.scope).Delete(ctx); err != nil {
		return err
	}
	if err := server.New(s.scope).DeletePrimitive(ctx); err != nil {
		return err
	}
	if hadBastion {
		s.scope.Event("BastionDeleted", "Deleted bastion")
	}
	if err := securitygroup.New(s.scope).DeletePrimitive(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Service) handleError(ctx context.Context, err error) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Error(err, "Failed to reconcile bastion", "cluster", s.scope.Name())
	s.scope.Warnf("FailedCreateBastion", "Failed to reconcile bastion: %v", err)
	return s.scope.CloudFailureResult(
		"BastionError",
		err,
		shared.RetryableErrorRequeueAfter,
		false,
		infrav1.ClusterBastionReadyCondition,
		infrav1.ClusterReadyCondition,
	)
}

func (s *Service) needsRecreate(cloudInitHash string) bool {
	if s.scope.BastionServerID() == "" {
		return false
	}
	return s.scope.BastionCloudInitHash() != cloudInitHash
}
