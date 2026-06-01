package controller

import (
	"context"

	v1alpha1 "github.com/ubag/ubag/deploy/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// AppReconciler reconciles App CRs against the gateway API.
type AppReconciler struct {
	client.Client
	Gateway GatewayClientInterface
}

// Reconcile implements reconcile.Reconciler for App.
func (r *AppReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var app v1alpha1.App
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion path
	if !app.DeletionTimestamp.IsZero() {
		if err := r.Gateway.DeleteApp(ctx, app.Spec.Name); err != nil {
			return reconcile.Result{RequeueAfter: gatewayBackoff}, err
		}
		return reconcile.Result{}, nil
	}

	// Idempotency check
	hash, err := HashSpec(app.Spec)
	if err != nil {
		return reconcile.Result{}, err
	}
	if app.Status.LastSyncedHash == hash {
		return reconcile.Result{}, nil
	}

	// Sync to gateway
	if err := r.Gateway.CreateOrUpdateApp(ctx, app.Spec); err != nil {
		return reconcile.Result{RequeueAfter: gatewayBackoff}, err
	}

	// Persist status
	app.Status.Ready = true
	app.Status.LastSyncedHash = hash
	app.Status.ObservedGeneration = app.Generation
	if err := r.Status().Update(ctx, &app); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}
