package object

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane-contrib/provider-kubernetes/apis/cluster/object/v1alpha2"
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
		FieldsV1:    metav1.NewFieldsV1(raw),
	}
}

func TestSSAResourceSyncerSyncResource(t *testing.T) {
	legacyManager := "crossplane-kubernetes-provider"
	errBoom := errors.New("boom")
	errNotFound := kerrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, testObjectName)
	legacySpecOwner := managedFieldsEntry(legacyManager, "", `{"f:spec":{"f:template":{}}}`)

	type args struct {
		observed     *unstructured.Unstructured
		migrationErr error
	}
	type want struct {
		patches []types.PatchType
		err     error
	}
	cases := map[string]struct {
		args args
		want want
	}{
		"NoObservedState": {
			args: args{},
			want: want{
				patches: []types.PatchType{types.ApplyPatchType},
			},
		},
		"NoLegacyManagers": {
			args: args{
				observed: observedJob(managedFieldsEntry("other-manager", "", `{"f:spec":{"f:template":{}}}`)),
			},
			want: want{
				patches: []types.PatchType{types.ApplyPatchType},
			},
		},
		"LegacyManagersMigrated": {
			args: args{
				observed: observedJob(legacySpecOwner),
			},
			want: want{
				patches: []types.PatchType{types.JSONPatchType, types.ApplyPatchType},
			},
		},
		"MigrationTargetAlreadyDeleted": {
			args: args{
				observed:     observedJob(legacySpecOwner),
				migrationErr: errNotFound,
			},
			want: want{
				patches: []types.PatchType{types.JSONPatchType, types.ApplyPatchType},
			},
		},
		"MigrationFails": {
			args: args{
				observed:     observedJob(legacySpecOwner),
				migrationErr: errBoom,
			},
			want: want{
				patches: []types.PatchType{types.JSONPatchType},
				err:     errors.Wrap(errors.Wrap(errBoom, "failed to patch managed fields upgrade"), "cannot upgrade field managers"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var patches []types.PatchType
			syncer := &SSAResourceSyncer{
				legacyCSAFieldManagers: sets.New(legacyManager),
				client: &test.MockClient{
					MockPatch: func(_ context.Context, _ client.Object, patch client.Patch, _ ...client.PatchOption) error {
						patches = append(patches, patch.Type())
						if patch.Type() == types.JSONPatchType {
							return tc.args.migrationErr
						}
						return nil
					},
				},
			}
			obj := &v1alpha2.Object{}
			obj.SetName(testObjectName)
			if tc.args.observed != nil {
				raw, err := tc.args.observed.MarshalJSON()
				if err != nil {
					t.Fatalf("cannot marshal observed state: %v", err)
				}
				obj.Status.AtProvider.Manifest.Raw = raw
			}

			_, gotErr := syncer.SyncResource(context.Background(), obj, observedJob())
			if diff := cmp.Diff(tc.want.err, gotErr, test.EquateErrors()); diff != "" {
				t.Errorf("SyncResource(...): -want error, +got error:\n%s", diff)
			}
			if diff := cmp.Diff(tc.want.patches, patches); diff != "" {
				t.Errorf("SyncResource(...): -want patches, +got patches:\n%s", diff)
			}
		})
	}
}

func observedJob(fields ...metav1.ManagedFieldsEntry) *unstructured.Unstructured {
	job := &unstructured.Unstructured{}
	job.SetAPIVersion("batch/v1")
	job.SetKind("Job")
	job.SetName(testObjectName)
	job.SetNamespace("default")
	job.SetManagedFields(fields)
	return job
}
