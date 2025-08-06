package object

import "regexp"

// CleanErr masks pointers in err when printing the message.
// errors.As and errors.Is can still find the original error.
// If err is nil, CleanErr returns nil.
func CleanErr(err error) error {
	if err == nil {
		return nil
	}
	return &cleanedError{cause: err}
}

type cleanedError struct {
	cause error
}

func (w *cleanedError) Error() string { return cleanErrorMessage(w.cause) }
func (w *cleanedError) Cause() error  { return w.cause }

// Unwrap provides compatibility for Go 1.13 error chains.
func (w *cleanedError) Unwrap() error { return w.cause }

var pointerRegex = regexp.MustCompile(`\(0x[0-9a-f]{5,}\)`)

// cleanErrorMessage returns the error reproducible error message for kubernetes errors by removing any pointers in the message.
//
// Given
//
//	cannot patch object: Job.batch "pi-1" is invalid: spec.template: Invalid value: core.PodTemplateSpec{... TerminationGracePeriodSeconds:(*int64)(0x4012805d98)...: field is immutable
//
// the pointer will be masked: TerminationGracePeriodSeconds:(*int64)(..ptr..)
func cleanErrorMessage(err error) string {
	oldString := err.Error()
	return pointerRegex.ReplaceAllString(oldString, "(..ptr..)")
}
