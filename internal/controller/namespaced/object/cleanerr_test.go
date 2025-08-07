package object

import (
	"testing"

	"github.com/pkg/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCleanWrap(t *testing.T) {
	const (
		messageWithoutPointer    = "this is 0123abc a normal error message (yeah)"
		messageWithPointer       = "cannot patch object: Job.batch 'pi-1' is invalid: spec.template: Invalid value: core.PodTemplateSpec{... TerminationGracePeriodSeconds:(*int64)(0x4012805d98): field is immutable"
		messageWithPointerMasked = "cannot patch object: Job.batch 'pi-1' is invalid: spec.template: Invalid value: core.PodTemplateSpec{... TerminationGracePeriodSeconds:(*int64)(..ptr..): field is immutable"
	)
	tests := []struct {
		name string
		in   error

		check func(t *testing.T, err error)
	}{
		{
			name: "nil is nil",
			in:   nil,
			check: func(t *testing.T, err error) {
				if err != nil {
					t.Errorf("expected nil error, got %+v", err)
				}
			},
		},
		{
			name: "normal error preserved",
			in:   errors.New(messageWithoutPointer),
			check: func(t *testing.T, err error) {
				if err.Error() != messageWithoutPointer {
					t.Errorf("expected %q error, got %v", messageWithoutPointer, err.Error())
				}
			},
		},
		{
			name: "pointer is masked",
			in:   errors.New(messageWithPointer),
			check: func(t *testing.T, err error) {
				if err.Error() != messageWithPointerMasked {
					t.Errorf("expected %q error, got %v", messageWithPointerMasked, err.Error())
				}
			},
		},
		{
			name: "errors.As preseved and kubernetes compatibility",
			in:   &kerrors.StatusError{ErrStatus: v1.Status{Reason: v1.StatusReasonAlreadyExists}},
			check: func(t *testing.T, err error) {
				if !kerrors.IsAlreadyExists(err) {
					t.Error("expected kerrors.IsAlreadyExists, got false")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, CleanErr(tt.in))
		})
	}
}
