package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type MigrationSource struct {
	RepositoryURL string `json:"repositoryURL"`
	Revision      string `json:"revision"`
}

type MigrationDestination struct {
	ProjectID string `json:"projectID"`
	Region    string `json:"region"`
	Runtime   string `json:"runtime"`
	Database  string `json:"database,omitempty"`
}

type MigrationSpec struct {
	Source      MigrationSource      `json:"source"`
	Destination MigrationDestination `json:"destination"`
}

type MigrationStatus struct {
	MigrationID        string             `json:"migrationID,omitempty"`
	Phase              string             `json:"phase,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

type Migration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MigrationSpec   `json:"spec,omitempty"`
	Status            MigrationStatus `json:"status,omitempty"`
}

type MigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Migration `json:"items"`
}

func (migration *Migration) DeepCopyInto(out *Migration) {
	*out = *migration
	migration.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	if migration.Status.Conditions != nil {
		out.Status.Conditions = append([]metav1.Condition(nil), migration.Status.Conditions...)
	}
}

func (migration *Migration) DeepCopy() *Migration {
	if migration == nil {
		return nil
	}
	out := new(Migration)
	migration.DeepCopyInto(out)
	return out
}

func (migration *Migration) DeepCopyObject() runtime.Object { return migration.DeepCopy() }

func (list *MigrationList) DeepCopyInto(out *MigrationList) {
	*out = *list
	list.ListMeta.DeepCopyInto(&out.ListMeta)
	if list.Items != nil {
		out.Items = make([]Migration, len(list.Items))
		for index := range list.Items {
			list.Items[index].DeepCopyInto(&out.Items[index])
		}
	}
}

func (list *MigrationList) DeepCopy() *MigrationList {
	if list == nil {
		return nil
	}
	out := new(MigrationList)
	list.DeepCopyInto(out)
	return out
}

func (list *MigrationList) DeepCopyObject() runtime.Object { return list.DeepCopy() }
