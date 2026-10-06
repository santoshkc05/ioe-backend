//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/adapters/postgres"
	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/postgres/pgtest"
)

var ctx = context.Background()

func TestAssets(t *testing.T) {
	repo := postgres.New(pgtest.New(t))
	at := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	video := domain.Asset{ID: 900, CourseID: 10, Kind: domain.KindVideo, CreatedBy: 7, CreatedAt: at}
	image := domain.Asset{ID: 901, CourseID: 10, Kind: domain.KindImage, CreatedBy: 7, CreatedAt: at}
	foreign := domain.Asset{ID: 902, CourseID: 11, Kind: domain.KindVideo, CreatedBy: 7, CreatedAt: at}
	for _, a := range []domain.Asset{video, image, foreign} {
		if err := repo.Insert(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.Find(ctx, video.ID)
	if err != nil || got != video {
		t.Fatalf("Find = %+v, %v", got, err)
	}
	if _, err := repo.Find(ctx, 999); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("Find missing err = %v", err)
	}

	kinds, err := repo.KindsInCourse(ctx, 10, []id.ID{900, 901, 902, 999})
	if err != nil || len(kinds) != 2 || kinds[900] != domain.KindVideo || kinds[901] != domain.KindImage {
		t.Fatalf("KindsInCourse = %v, %v", kinds, err)
	}
	if kinds, err := repo.KindsInCourse(ctx, 10, nil); err != nil || len(kinds) != 0 {
		t.Fatalf("KindsInCourse(nil) = %v, %v", kinds, err)
	}

	if err := repo.Delete(ctx, video.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, video.ID); err != nil {
		t.Fatalf("second delete err = %v", err)
	}
	if _, err := repo.Find(ctx, video.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("Find after delete err = %v", err)
	}
}
