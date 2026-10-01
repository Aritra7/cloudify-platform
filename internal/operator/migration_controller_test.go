package operator

import (
	"context"
	"testing"
	"time"

	cloudifyv1alpha1 "github.com/Aritra7/cloudify-platform/api/v1alpha1"
	cloudifyprovider "github.com/Aritra7/cloudify-platform/internal/provider"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeMigrationAPI struct {
	migration      cloudifyprovider.Migration
	created        int
	cancelled      int
	idempotencyKey string
}

func (api *fakeMigrationAPI) CreateMigration(
	_ context.Context, _ cloudifyprovider.CreateMigrationRequest, key string,
) (cloudifyprovider.Migration, error) {
	api.created++
	api.idempotencyKey = key
	return api.migration, nil
}

func (api *fakeMigrationAPI) GetMigration(context.Context, string) (cloudifyprovider.Migration, error) {
	return api.migration, nil
}

func (api *fakeMigrationAPI) CancelMigration(context.Context, string) (cloudifyprovider.Migration, error) {
	api.cancelled++
	api.migration.Status = "cancelled"
	return api.migration, nil
}

func TestMigrationReconcilerCreatesAndObservesOperation(t *testing.T) {
	t.Parallel()
	reconciler, api, key := testReconciler(t, &cloudifyv1alpha1.Migration{
		ObjectMeta: metav1.ObjectMeta{Name: "application", Namespace: "default", Generation: 1},
		Spec: cloudifyv1alpha1.MigrationSpec{
			Source:      cloudifyv1alpha1.MigrationSource{RepositoryURL: "https://github.com/example/application", Revision: "main"},
			Destination: cloudifyv1alpha1.MigrationDestination{ProjectID: "example-project", Region: "us-central1", Runtime: "cloud-run"},
		},
	})
	ctx := context.Background()
	request := ctrl.Request{NamespacedName: key}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("create migration: %v", err)
	}
	var stored cloudifyv1alpha1.Migration
	if err := reconciler.Get(ctx, key, &stored); err != nil {
		t.Fatalf("get migration: %v", err)
	}
	if api.created != 1 || api.idempotencyKey != "kubernetes-default-application-1" || stored.Status.MigrationID != "migration-1" || stored.Status.ObservedGeneration != 1 {
		t.Fatalf("api=%#v status=%#v", api, stored.Status)
	}
	api.migration.Status = "succeeded"
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("observe migration: %v", err)
	}
	if err := reconciler.Get(ctx, key, &stored); err != nil {
		t.Fatalf("get completed migration: %v", err)
	}
	if stored.Status.Phase != "succeeded" || len(stored.Status.Conditions) == 0 || stored.Status.Conditions[len(stored.Status.Conditions)-1].Type != "Ready" {
		t.Fatalf("completed status = %#v", stored.Status)
	}
}

func TestMigrationReconcilerFinalizerCancelsActiveOperation(t *testing.T) {
	t.Parallel()
	now := metav1.NewTime(time.Unix(100, 0).UTC())
	reconciler, api, key := testReconciler(t, &cloudifyv1alpha1.Migration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "application", Namespace: "default", Generation: 1,
			Finalizers: []string{MigrationFinalizer}, DeletionTimestamp: &now,
		},
		Status: cloudifyv1alpha1.MigrationStatus{MigrationID: "migration-1", Phase: "running", ObservedGeneration: 1},
	})
	ctx := context.Background()
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("cancel deletion: %v", err)
	}
	if api.cancelled != 1 {
		t.Fatalf("cancelled = %d", api.cancelled)
	}
	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("complete deletion: %v", err)
	}
	var stored cloudifyv1alpha1.Migration
	err := reconciler.Get(ctx, key, &stored)
	if err == nil && containsString(stored.Finalizers, MigrationFinalizer) {
		t.Fatalf("finalizer was not removed: %#v", stored.Finalizers)
	}
}

func testReconciler(
	t *testing.T, migration *cloudifyv1alpha1.Migration,
) (*MigrationReconciler, *fakeMigrationAPI, types.NamespacedName) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cloudifyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	kubernetesClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&cloudifyv1alpha1.Migration{}).WithObjects(migration).Build()
	api := &fakeMigrationAPI{migration: cloudifyprovider.Migration{ID: "migration-1", Status: "running"}}
	reconciler := &MigrationReconciler{
		Client: kubernetesClient, API: api, PollInterval: time.Second,
		Now: func() time.Time { return time.Unix(200, 0).UTC() },
	}
	return reconciler, api, types.NamespacedName{Name: migration.Name, Namespace: migration.Namespace}
}
