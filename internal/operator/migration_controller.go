package operator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	cloudifyv1alpha1 "github.com/Aritra7/cloudify-platform/api/v1alpha1"
	cloudifyprovider "github.com/Aritra7/cloudify-platform/internal/provider"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const MigrationFinalizer = "cloudify.dev/migration-finalizer"

type MigrationAPI interface {
	CreateMigration(context.Context, cloudifyprovider.CreateMigrationRequest, string) (cloudifyprovider.Migration, error)
	GetMigration(context.Context, string) (cloudifyprovider.Migration, error)
	CancelMigration(context.Context, string) (cloudifyprovider.Migration, error)
}

type MigrationReconciler struct {
	client.Client
	API          MigrationAPI
	PollInterval time.Duration
	Now          func() time.Time
	Recorder     record.EventRecorder
}

func (reconciler *MigrationReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var migration cloudifyv1alpha1.Migration
	if err := reconciler.Get(ctx, request.NamespacedName, &migration); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if reconciler.API == nil {
		return ctrl.Result{}, errors.New("migration reconciler requires a control-plane API")
	}
	if reconciler.PollInterval <= 0 {
		reconciler.PollInterval = 5 * time.Second
	}
	if reconciler.Now == nil {
		reconciler.Now = func() time.Time { return time.Now().UTC() }
	}
	if !migration.DeletionTimestamp.IsZero() {
		return reconciler.reconcileDeletion(ctx, &migration)
	}
	if !containsString(migration.Finalizers, MigrationFinalizer) {
		migration.Finalizers = append(migration.Finalizers, MigrationFinalizer)
		return ctrl.Result{}, reconciler.Update(ctx, &migration)
	}
	if migration.Status.MigrationID == "" {
		return reconciler.create(ctx, &migration)
	}
	remote, err := reconciler.API.GetMigration(ctx, migration.Status.MigrationID)
	if notFound(err) {
		migration.Status.MigrationID = ""
		migration.Status.Phase = ""
		return ctrl.Result{}, reconciler.updateStatus(ctx, &migration, "Progressing", metav1.ConditionFalse, "RemoteMissing", "remote migration disappeared; recreation is scheduled")
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if migration.Status.ObservedGeneration != migration.Generation {
		if !terminal(remote.Status) {
			if _, err := reconciler.API.CancelMigration(ctx, remote.ID); err != nil && !invalidTransition(err) {
				return ctrl.Result{}, err
			}
			migration.Status.Phase = "cancelling"
			if err := reconciler.updateStatus(ctx, &migration, "Progressing", metav1.ConditionTrue, "Replacing", "specification changed; cancelling the previous migration"); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: reconciler.PollInterval}, nil
		}
		migration.Status.MigrationID = ""
		migration.Status.Phase = ""
		if err := reconciler.Status().Update(ctx, &migration); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	migration.Status.Phase = remote.Status
	conditionStatus, reason, message := metav1.ConditionTrue, "Running", "migration is in progress"
	conditionType := "Progressing"
	if remote.Status == "succeeded" {
		conditionType, reason, message = "Ready", "Succeeded", "migration completed successfully"
	} else if remote.Status == "failed" || remote.Status == "cancelled" {
		conditionType, conditionStatus, reason, message = "Ready", metav1.ConditionFalse, "Terminal", "migration reached terminal status "+remote.Status
	}
	if err := reconciler.updateStatus(ctx, &migration, conditionType, conditionStatus, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	if terminal(remote.Status) {
		reconciler.event(&migration, "Normal", "MigrationTerminal", "Migration reached terminal status "+remote.Status)
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: reconciler.PollInterval}, nil
}

func (reconciler *MigrationReconciler) create(ctx context.Context, migration *cloudifyv1alpha1.Migration) (ctrl.Result, error) {
	remote, err := reconciler.API.CreateMigration(ctx, cloudifyprovider.CreateMigrationRequest{
		Source: cloudifyprovider.Source{
			RepositoryURL: migration.Spec.Source.RepositoryURL, Revision: migration.Spec.Source.Revision,
		},
		Destination: cloudifyprovider.Destination{
			Provider: "gcp", ProjectID: migration.Spec.Destination.ProjectID,
			Region: migration.Spec.Destination.Region, Runtime: migration.Spec.Destination.Runtime,
			Database: migration.Spec.Destination.Database,
		},
	}, fmt.Sprintf("kubernetes-%s-%s-%d", migration.Namespace, migration.Name, migration.Generation))
	if err != nil {
		return ctrl.Result{}, err
	}
	migration.Status.MigrationID = remote.ID
	migration.Status.Phase = remote.Status
	migration.Status.ObservedGeneration = migration.Generation
	if err := reconciler.updateStatus(ctx, migration, "Progressing", metav1.ConditionTrue, "Submitted", "migration was accepted by the control plane"); err != nil {
		return ctrl.Result{}, err
	}
	reconciler.event(migration, "Normal", "MigrationSubmitted", "Migration was accepted by the Cloudify control plane")
	return ctrl.Result{RequeueAfter: reconciler.PollInterval}, nil
}

func (reconciler *MigrationReconciler) reconcileDeletion(ctx context.Context, migration *cloudifyv1alpha1.Migration) (ctrl.Result, error) {
	if !containsString(migration.Finalizers, MigrationFinalizer) {
		return ctrl.Result{}, nil
	}
	if migration.Status.MigrationID != "" {
		remote, err := reconciler.API.GetMigration(ctx, migration.Status.MigrationID)
		if err != nil && !notFound(err) {
			return ctrl.Result{}, err
		}
		if err == nil && !terminal(remote.Status) {
			if _, err := reconciler.API.CancelMigration(ctx, remote.ID); err != nil && !invalidTransition(err) {
				return ctrl.Result{}, err
			}
			reconciler.event(migration, "Normal", "MigrationCancelled", "Cancellation was requested before finalizer removal")
			return ctrl.Result{RequeueAfter: reconciler.PollInterval}, nil
		}
	}
	migration.Finalizers = removeString(migration.Finalizers, MigrationFinalizer)
	return ctrl.Result{}, reconciler.Update(ctx, migration)
}

func (reconciler *MigrationReconciler) event(migration *cloudifyv1alpha1.Migration, eventType, reason, message string) {
	if reconciler.Recorder != nil {
		reconciler.Recorder.Event(migration, eventType, reason, message)
	}
}

func (reconciler *MigrationReconciler) updateStatus(
	ctx context.Context, migration *cloudifyv1alpha1.Migration,
	conditionType string, status metav1.ConditionStatus, reason, message string,
) error {
	meta.SetStatusCondition(&migration.Status.Conditions, metav1.Condition{
		Type: conditionType, Status: status, Reason: reason, Message: message,
		ObservedGeneration: migration.Generation, LastTransitionTime: metav1.NewTime(reconciler.Now()),
	})
	return reconciler.Status().Update(ctx, migration)
}

func (reconciler *MigrationReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&cloudifyv1alpha1.Migration{}).Complete(reconciler)
}

func terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

func notFound(err error) bool {
	var apiErr *cloudifyprovider.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func invalidTransition(err error) bool {
	var apiErr *cloudifyprovider.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func removeString(values []string, remove string) []string {
	result := values[:0]
	for _, value := range values {
		if value != remove {
			result = append(result, value)
		}
	}
	return result
}
