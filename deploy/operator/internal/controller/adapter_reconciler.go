package controller

import (
	"context"

	v1alpha1 "github.com/ubag/ubag/deploy/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// AdapterReconciler reconciles Adapter CRs against the gateway API.
type AdapterReconciler struct {
	client.Client
	Gateway GatewayClientInterface
}

// Reconcile implements reconcile.Reconciler for Adapter.
func (r *AdapterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var adapter v1alpha1.Adapter
	if err := r.Get(ctx, req.NamespacedName, &adapter); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion path
	if !adapter.DeletionTimestamp.IsZero() {
		if err := r.Gateway.DeleteAdapter(ctx, adapter.Spec.Name); err != nil {
			return reconcile.Result{RequeueAfter: gatewayBackoff}, err
		}
		return reconcile.Result{}, nil
	}

	// Idempotency check
	hash, err := HashSpec(adapter.Spec)
	if err != nil {
		return reconcile.Result{}, err
	}
	if adapter.Status.LastSyncedHash == hash {
		return reconcile.Result{}, nil
	}

	// Sync to gateway
	if err := r.Gateway.CreateOrUpdateAdapter(ctx, adapter.Spec); err != nil {
		return reconcile.Result{RequeueAfter: gatewayBackoff}, err
	}

	// Persist status
	adapter.Status.Ready = true
	adapter.Status.LastSyncedHash = hash
	adapter.Status.ObservedGeneration = adapter.Generation
	if err := r.Status().Update(ctx, &adapter); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}
