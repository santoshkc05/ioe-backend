package main

import (
	"context"
	"errors"
	"testing"

	certificateapp "github.com/santoshkc2200/ioe-backend/internal/certificate/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	progressapp "github.com/santoshkc2200/ioe-backend/internal/progress/app"
)

type progressStub struct {
	done bool
	err  error
}

func (s progressStub) IsComplete(context.Context, id.ID, id.ID) (bool, error) { return s.done, s.err }

func TestCertificateProgressMapsNotFound(t *testing.T) {
	ctx := context.Background()
	if _, err := (certificateProgress{svc: progressStub{err: progressapp.ErrNotFound}}).IsComplete(ctx, 1, 2); !errors.Is(err, certificateapp.ErrNotFound) {
		t.Fatalf("unknown course: err = %v, want certificate ErrNotFound", err)
	}
	failed := errors.New("db down")
	if _, err := (certificateProgress{svc: progressStub{err: failed}}).IsComplete(ctx, 1, 2); !errors.Is(err, failed) {
		t.Fatalf("storage failure: err = %v", err)
	}
	if done, err := (certificateProgress{svc: progressStub{done: true}}).IsComplete(ctx, 1, 2); err != nil || !done {
		t.Fatalf("complete: %v, %v", done, err)
	}
}
