package controller

import (
	"context"

	v1alpha1 "github.com/ubag/ubag/deploy/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TemplateReconciler reconciles Template CRs against the gateway API.
type TemplateReconciler struct {
	client.Client
	Gateway GatewayClientInterface
}

// Reconcile implements reconcile.Reconciler for Template.
func (r *TemplateReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var tmpl v1alpha1.Template
	if err := r.Get(ctx, req.NamespacedName, &tmpl); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion path
	if !tmpl.DeletionTimestamp.IsZero() {
		if err := r.Gateway.DeleteTemplate(ctx, tmpl.Spec.Name); err != nil {
			return reconcile.Result{RequeueAfter: gatewayBackoff}, err
		}
		return reconcile.Result{}, nil
	}

	// Idempotency check
	hash, err := HashSpec(tmpl.Spec)
	if err != nil {
		return reconcile.Result{}, err
	}
	if tmpl.Status.LastSyncedHash == hash {
		return reconcile.Result{}, nil
	}

	// Sync to gateway
	if err := r.Gateway.CreateOrUpdateTemplate(ctx, tmpl.Spec); err != nil {
		return reconcile.Result{RequeueAfter: gatewayBackoff}, err
	}

	// Persist status
	tmpl.Status.Ready = true
	tmpl.Status.LastSyncedHash = hash
	tmpl.Status.ObservedGeneration = tmpl.Generation
	if err := r.Status().Update(ctx, &tmpl); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}
