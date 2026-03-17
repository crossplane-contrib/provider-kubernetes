package object

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestNeedSSAFieldManagerUpgrade(t *testing.T) {
	legacyManager := "crossplane-kubernetes-provider"
	syncer := &SSAResourceSyncer{
		legacyCSAFieldManagers: sets.New(legacyManager),
	}

	tests := []struct {
		name   string
		fields []metav1.ManagedFieldsEntry
		want   bool
	}{
		{
			name: "StatusOnlyUpdateIsIgnored",
			fields: []metav1.ManagedFieldsEntry{
				managedFieldsEntry(legacyManager, "status", `{"f:status":{".":{},"f:users":{}}}`),
			},
			want: false,
		},
		{
			name: "FinalizersOnlyUpdateIsIgnored",
			fields: []metav1.ManagedFieldsEntry{
				managedFieldsEntry(legacyManager, "", `{"f:metadata":{"f:finalizers":{".":{},"v:\"in-use.crossplane.io\"":{}}}}`),
			},
			want: false,
		},
		{
			name: "SpecUpdateTriggersUpgrade",
			fields: []metav1.ManagedFieldsEntry{
				managedFieldsEntry(legacyManager, "", `{"f:spec":{"f:credentials":{}}}`),
			},
			want: true,
		},
		{
			name: "TopLevelDataUpdateTriggersUpgrade",
			fields: []metav1.ManagedFieldsEntry{
				managedFieldsEntry(legacyManager, "", `{"f:data":{"f:key":{}}}`),
			},
			want: true,
		},
		{
			name: "NonLegacyManagerIsIgnored",
			fields: []metav1.ManagedFieldsEntry{
				managedFieldsEntry("other-manager", "", `{"f:spec":{"f:credentials":{}}}`),
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			obj.SetManagedFields(tc.fields)
			if got := syncer.needSSAFieldManagerUpgrade(obj); got != tc.want {
				t.Fatalf("needSSAFieldManagerUpgrade() = %v, want %v", got, tc.want)
			}
		})
	}
}

func managedFieldsEntry(manager, subresource, raw string) metav1.ManagedFieldsEntry {
	return metav1.ManagedFieldsEntry{
		Manager:     manager,
		Operation:   metav1.ManagedFieldsOperationUpdate,
		Subresource: subresource,
		FieldsType:  "FieldsV1",
		FieldsV1: &metav1.FieldsV1{
			Raw: []byte(raw),
		},
	}
}
