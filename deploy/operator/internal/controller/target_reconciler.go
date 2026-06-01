package controller

import (
	"context"
	"time"

	v1alpha1 "github.com/ubag/ubag/deploy/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const gatewayBackoff = 30 * time.Second

// TargetReconciler reconciles Target CRs against the gateway API.
type TargetReconciler struct {
	client.Client
	Gateway GatewayClientInterface
}

// Reconcile implements reconcile.Reconciler for Target.
func (r *TargetReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var target v1alpha1.Target
	if err := r.Get(ctx, req.NamespacedName, &target); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion path
	if !target.DeletionTimestamp.IsZero() {
		if err := r.Gateway.DeleteTarget(ctx, target.Spec.Name); err != nil {
			return reconcile.Result{RequeueAfter: gatewayBackoff}, err
		}
		return reconcile.Result{}, nil
	}

	// Idempotency check
	hash, err := HashSpec(target.Spec)
	if err != nil {
		return reconcile.Result{}, err
	}
	if target.Status.LastSyncedHash == hash {
		return reconcile.Result{}, nil
	}

	// Sync to gateway
	if err := r.Gateway.CreateOrUpdateTarget(ctx, target.Spec); err != nil {
		return reconcile.Result{RequeueAfter: gatewayBackoff}, err
	}

	// Persist status
	target.Status.Ready = true
	target.Status.LastSyncedHash = hash
	target.Status.ObservedGeneration = target.Generation
	if err := r.Status().Update(ctx, &target); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}
