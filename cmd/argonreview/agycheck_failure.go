package main

import (
	"errors"
	"fmt"
	"strings"
)

type agyCheckStep string

const (
	agyCheckValidation   agyCheckStep = "validation failed"
	agyCheckPinning      agyCheckStep = "pin failed"
	agyCheckCollection   agyCheckStep = "collection failed"
	agyCheckRestoration  agyCheckStep = "restoration failed"
	agyCheckCleanup      agyCheckStep = "cleanup failed"
	agyCheckExecution    agyCheckStep = "execution failed"
	agyCheckInterruption agyCheckStep = "agy-check interrupted"
)

// Error keeps step labels independent of raw diagnostic text.
func (s agyCheckStep) Error() string { return string(s) }

func agyCheckFailed(step agyCheckStep, err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(step, err)
}

type agyCheckStepLabels []error

// Error keeps multiple step labels on one report line.
func (labels agyCheckStepLabels) Error() string {
	text := make([]string, len(labels))
	for i, label := range labels {
		text[i] = label.Error()
	}
	return strings.Join(text, "; ")
}

// Unwrap preserves step identity without retaining raw causes.
func (labels agyCheckStepLabels) Unwrap() []error { return labels }

// Only fixed labels cross into results: wrapped errors can hold host paths,
// environment values or transcript content, and printable text is not redaction.
func agyCheckRowFailure(err error) error {
	var details []error
	for _, step := range []agyCheckStep{agyCheckValidation, agyCheckPinning, agyCheckCollection, agyCheckKeeping, agyCheckRestoration, agyCheckCleanup, agyCheckExecution, agyCheckInterruption} {
		if errors.Is(err, step) {
			details = append(details, step)
		}
	}
	if len(details) == 0 {
		details = append(details, agyCheckExecution)
	}
	return fmt.Errorf("row failed: %w", agyCheckStepLabels(details))
}
