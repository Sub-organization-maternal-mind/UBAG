package controller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	v1alpha1 "github.com/ubag/ubag/deploy/operator/api/v1alpha1"
	"github.com/ubag/ubag/deploy/operator/internal/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ---------------------------------------------------------------------------
// Mock gateway client
// ---------------------------------------------------------------------------

type mockGateway struct {
	targetCreated   int
	targetDeleted   int
	adapterCreated  int
	adapterDeleted  int
	templateCreated int
	templateDeleted int
	appCreated      int
	appDeleted      int
	err             error
}

func (m *mockGateway) CreateOrUpdateTarget(_ context.Context, _ v1alpha1.TargetSpec) error {
	m.targetCreated++
	return m.err
}
func (m *mockGateway) DeleteTarget(_ context.Context, _ string) error {
	m.targetDeleted++
	return m.err
}
func (m *mockGateway) CreateOrUpdateAdapter(_ context.Context, _ v1alpha1.AdapterSpec) error {
	m.adapterCreated++
	return m.err
}
func (m *mockGateway) DeleteAdapter(_ context.Context, _ string) error {
	m.adapterDeleted++
	return m.err
}
func (m *mockGateway) CreateOrUpdateTemplate(_ context.Context, _ v1alpha1.TemplateSpec) error {
	m.templateCreated++
	return m.err
}
func (m *mockGateway) DeleteTemplate(_ context.Context, _ string) error {
	m.templateDeleted++
	return m.err
}
func (m *mockGateway) CreateOrUpdateApp(_ context.Context, _ v1alpha1.AppSpec) error {
	m.appCreated++
	return m.err
}
func (m *mockGateway) DeleteApp(_ context.Context, _ string) error {
	m.appDeleted++
	return m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

func newFakeClient(scheme *runtime.Scheme, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Target{}, &v1alpha1.Adapter{}, &v1alpha1.Template{}, &v1alpha1.App{}).
		WithObjects(objs...).
		Build()
}

func makeTarget(name string) *v1alpha1.Target {
	return &v1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: v1alpha1.TargetSpec{
			Name:  name,
			URL:   "http://llm.example.com",
			Model: "gpt-4",
			Tags:  []string{"prod"},
		},
	}
}

func req(name string) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
	}
}

// ---------------------------------------------------------------------------
// Target reconciler tests
// ---------------------------------------------------------------------------

func TestTargetReconcile_Create(t *testing.T) {
	scheme := newScheme()
	target := makeTarget("t1")
	fakeClient := newFakeClient(scheme, target)
	gw := &mockGateway{}
	r := &controller.TargetReconciler{Client: fakeClient, Gateway: gw}

	result, err := r.Reconcile(context.Background(), req("t1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %v", result.RequeueAfter)
	}
	if gw.targetCreated != 1 {
		t.Errorf("expected 1 gateway create call, got %d", gw.targetCreated)
	}

	// Status should have been written
	var updated v1alpha1.Target
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "t1", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	if !updated.Status.Ready {
		t.Error("expected Status.Ready = true")
	}
	if updated.Status.LastSyncedHash == "" {
		t.Error("expected LastSyncedHash to be populated")
	}
}

func TestTargetReconcile_NoOp(t *testing.T) {
	scheme := newScheme()
	target := makeTarget("t2")

	// Pre-compute the hash the reconciler will produce
	hash, err := controller.HashSpec(target.Spec)
	if err != nil {
		t.Fatalf("HashSpec: %v", err)
	}
	target.Status.LastSyncedHash = hash

	fakeClient := newFakeClient(scheme, target)
	gw := &mockGateway{}
	r := &controller.TargetReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("t2")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.targetCreated != 0 {
		t.Errorf("expected 0 gateway calls on no-op, got %d", gw.targetCreated)
	}
}

func TestTargetReconcile_GatewayError(t *testing.T) {
	scheme := newScheme()
	target := makeTarget("t3")
	fakeClient := newFakeClient(scheme, target)
	gw := &mockGateway{err: errors.New("gateway unavailable")}
	r := &controller.TargetReconciler{Client: fakeClient, Gateway: gw}

	result, err := r.Reconcile(context.Background(), req("t3"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected 30s requeue backoff, got %v", result.RequeueAfter)
	}
}

func TestTargetReconcile_Delete(t *testing.T) {
	scheme := newScheme()
	now := metav1.Now()
	target := makeTarget("t4")
	target.DeletionTimestamp = &now
	// fake client requires a finalizer for the deletion timestamp to be set
	target.Finalizers = []string{"ubag.dev/cleanup"}
	fakeClient := newFakeClient(scheme, target)
	gw := &mockGateway{}
	r := &controller.TargetReconciler{Client: fakeClient, Gateway: gw}

	result, err := r.Reconcile(context.Background(), req("t4"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %v", result.RequeueAfter)
	}
	if gw.targetDeleted != 1 {
		t.Errorf("expected 1 gateway delete call, got %d", gw.targetDeleted)
	}
}

func TestTargetReconcile_NotFound(t *testing.T) {
	scheme := newScheme()
	fakeClient := newFakeClient(scheme) // no objects
	gw := &mockGateway{}
	r := &controller.TargetReconciler{Client: fakeClient, Gateway: gw}

	result, err := r.Reconcile(context.Background(), req("nonexistent"))
	if err != nil {
		t.Fatalf("not-found should be ignored, got: %v", err)
	}
	if result.Requeue {
		t.Error("should not requeue on not-found")
	}
}

// ---------------------------------------------------------------------------
// Adapter reconciler tests
// ---------------------------------------------------------------------------

func TestAdapterReconcile_Create(t *testing.T) {
	scheme := newScheme()
	adapter := &v1alpha1.Adapter{
		ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: "default"},
		Spec: v1alpha1.AdapterSpec{
			Name:   "a1",
			Type:   "openai",
			Config: map[string]string{"api_key": "secret"},
		},
	}
	fakeClient := newFakeClient(scheme, adapter)
	gw := &mockGateway{}
	r := &controller.AdapterReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("a1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.adapterCreated != 1 {
		t.Errorf("expected 1 adapter create, got %d", gw.adapterCreated)
	}
}

func TestAdapterReconcile_NoOp(t *testing.T) {
	scheme := newScheme()
	adapter := &v1alpha1.Adapter{
		ObjectMeta: metav1.ObjectMeta{Name: "a2", Namespace: "default"},
		Spec: v1alpha1.AdapterSpec{Name: "a2", Type: "bedrock"},
	}
	hash, _ := controller.HashSpec(adapter.Spec)
	adapter.Status.LastSyncedHash = hash
	fakeClient := newFakeClient(scheme, adapter)
	gw := &mockGateway{}
	r := &controller.AdapterReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("a2")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.adapterCreated != 0 {
		t.Errorf("expected 0 adapter create on no-op, got %d", gw.adapterCreated)
	}
}

func TestAdapterReconcile_Delete(t *testing.T) {
	scheme := newScheme()
	now := metav1.Now()
	adapter := &v1alpha1.Adapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "a3",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{"ubag.dev/cleanup"},
		},
		Spec: v1alpha1.AdapterSpec{Name: "a3", Type: "ollama"},
	}
	fakeClient := newFakeClient(scheme, adapter)
	gw := &mockGateway{}
	r := &controller.AdapterReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("a3")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.adapterDeleted != 1 {
		t.Errorf("expected 1 adapter delete, got %d", gw.adapterDeleted)
	}
}

// ---------------------------------------------------------------------------
// Template reconciler tests
// ---------------------------------------------------------------------------

func TestTemplateReconcile_Create(t *testing.T) {
	scheme := newScheme()
	tmpl := &v1alpha1.Template{
		ObjectMeta: metav1.ObjectMeta{Name: "tmpl1", Namespace: "default"},
		Spec: v1alpha1.TemplateSpec{
			Name:      "tmpl1",
			Content:   "Hello {{name}}!",
			Variables: []string{"name"},
		},
	}
	fakeClient := newFakeClient(scheme, tmpl)
	gw := &mockGateway{}
	r := &controller.TemplateReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("tmpl1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.templateCreated != 1 {
		t.Errorf("expected 1 template create, got %d", gw.templateCreated)
	}
}

func TestTemplateReconcile_NoOp(t *testing.T) {
	scheme := newScheme()
	tmpl := &v1alpha1.Template{
		ObjectMeta: metav1.ObjectMeta{Name: "tmpl2", Namespace: "default"},
		Spec:       v1alpha1.TemplateSpec{Name: "tmpl2", Content: "Hello!"},
	}
	hash, _ := controller.HashSpec(tmpl.Spec)
	tmpl.Status.LastSyncedHash = hash
	fakeClient := newFakeClient(scheme, tmpl)
	gw := &mockGateway{}
	r := &controller.TemplateReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("tmpl2")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.templateCreated != 0 {
		t.Errorf("expected 0 template create on no-op, got %d", gw.templateCreated)
	}
}

func TestTemplateReconcile_Delete(t *testing.T) {
	scheme := newScheme()
	now := metav1.Now()
	tmpl := &v1alpha1.Template{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "tmpl3",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{"ubag.dev/cleanup"},
		},
		Spec: v1alpha1.TemplateSpec{Name: "tmpl3", Content: "Bye!"},
	}
	fakeClient := newFakeClient(scheme, tmpl)
	gw := &mockGateway{}
	r := &controller.TemplateReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("tmpl3")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.templateDeleted != 1 {
		t.Errorf("expected 1 template delete, got %d", gw.templateDeleted)
	}
}

// ---------------------------------------------------------------------------
// App reconciler tests
// ---------------------------------------------------------------------------

func TestAppReconcile_Create(t *testing.T) {
	scheme := newScheme()
	app := &v1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "app1", Namespace: "default"},
		Spec: v1alpha1.AppSpec{
			Name:        "app1",
			Description: "my app",
			Targets:     []string{"target-a"},
		},
	}
	fakeClient := newFakeClient(scheme, app)
	gw := &mockGateway{}
	r := &controller.AppReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("app1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.appCreated != 1 {
		t.Errorf("expected 1 app create, got %d", gw.appCreated)
	}
}

func TestAppReconcile_NoOp(t *testing.T) {
	scheme := newScheme()
	app := &v1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "app2", Namespace: "default"},
		Spec:       v1alpha1.AppSpec{Name: "app2", Description: "stable"},
	}
	hash, _ := controller.HashSpec(app.Spec)
	app.Status.LastSyncedHash = hash
	fakeClient := newFakeClient(scheme, app)
	gw := &mockGateway{}
	r := &controller.AppReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("app2")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.appCreated != 0 {
		t.Errorf("expected 0 app create on no-op, got %d", gw.appCreated)
	}
}

func TestAppReconcile_GatewayError(t *testing.T) {
	scheme := newScheme()
	app := &v1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{Name: "app3", Namespace: "default"},
		Spec:       v1alpha1.AppSpec{Name: "app3"},
	}
	fakeClient := newFakeClient(scheme, app)
	gw := &mockGateway{err: errors.New("timeout")}
	r := &controller.AppReconciler{Client: fakeClient, Gateway: gw}

	result, err := r.Reconcile(context.Background(), req("app3"))
	if err == nil {
		t.Fatal("expected error from gateway")
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected 30s requeue, got %v", result.RequeueAfter)
	}
}

func TestAppReconcile_Delete(t *testing.T) {
	scheme := newScheme()
	now := metav1.Now()
	app := &v1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "app4",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{"ubag.dev/cleanup"},
		},
		Spec: v1alpha1.AppSpec{Name: "app4"},
	}
	fakeClient := newFakeClient(scheme, app)
	gw := &mockGateway{}
	r := &controller.AppReconciler{Client: fakeClient, Gateway: gw}

	if _, err := r.Reconcile(context.Background(), req("app4")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw.appDeleted != 1 {
		t.Errorf("expected 1 app delete, got %d", gw.appDeleted)
	}
}

// ---------------------------------------------------------------------------
// HashSpec unit test
// ---------------------------------------------------------------------------

func TestHashSpec_Stable(t *testing.T) {
	spec := v1alpha1.TargetSpec{Name: "x", URL: "http://a", Model: "m", Tags: []string{"t1"}}
	h1, err1 := controller.HashSpec(spec)
	h2, err2 := controller.HashSpec(spec)
	if err1 != nil || err2 != nil {
		t.Fatalf("HashSpec errors: %v, %v", err1, err2)
	}
	if h1 != h2 {
		t.Errorf("hash is not stable: %q != %q", h1, h2)
	}
	if len(h1) != 12 {
		t.Errorf("expected 12-char hash, got len=%d: %q", len(h1), h1)
	}
}

func TestHashSpec_Different(t *testing.T) {
	spec1 := v1alpha1.TargetSpec{Name: "x", URL: "http://a"}
	spec2 := v1alpha1.TargetSpec{Name: "x", URL: "http://b"}
	h1, _ := controller.HashSpec(spec1)
	h2, _ := controller.HashSpec(spec2)
	if h1 == h2 {
		t.Error("different specs should produce different hashes")
	}
}
