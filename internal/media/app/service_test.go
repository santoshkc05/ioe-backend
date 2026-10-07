package app_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	videoA = domain.Asset{ID: 900, CourseID: courseA, Kind: domain.KindVideo, CreatedBy: owner.UserID, CreatedAt: t0}
	imageA = domain.Asset{ID: 901, CourseID: courseA, Kind: domain.KindImage, CreatedBy: owner.UserID, CreatedAt: t0}
	videoB = domain.Asset{ID: 902, CourseID: courseB, Kind: domain.KindVideo, CreatedBy: owner.UserID, CreatedAt: t0}
)

func TestCreateUpload(t *testing.T) {
	repo, remote := newRepo(), &fakeRemote{upload: app.Upload{AssetID: 777, UploadID: "u"}}
	svc := newService(t, repo, remote, fakeAccess{})
	up, err := svc.CreateUpload(ctx, owner, courseA, app.UploadInput{Kind: "video", ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9})
	if err != nil || up.AssetID != 777 {
		t.Fatalf("upload = %+v, %v", up, err)
	}
	c := remote.created[0]
	if c.Kind != domain.KindVideo || c.OwnerID != owner.UserID || c.CourseID != courseA ||
		!strings.HasPrefix(c.IdempotencyKey, "ioe:upload:") || len(c.IdempotencyKey) <= len("ioe:upload:") {
		t.Fatalf("remote create = %+v", c)
	}
	want := domain.Asset{ID: 777, CourseID: courseA, Kind: domain.KindVideo, CreatedBy: owner.UserID, CreatedAt: t0}
	if repo.rows[777] != want {
		t.Fatalf("row = %+v", repo.rows[777])
	}
	if _, err := svc.CreateUpload(ctx, owner, courseA, app.UploadInput{Kind: "video", ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9}); err != nil {
		t.Fatal(err)
	}
	if remote.created[0].IdempotencyKey == remote.created[1].IdempotencyKey {
		t.Fatal("idempotency key reused across uploads")
	}
}

func TestCreateUploadRejects(t *testing.T) {
	valid := app.UploadInput{Kind: "image", ContentType: "image/png", Filename: "a.png", SizeBytes: 1}
	tests := []struct {
		name   string
		in     app.UploadInput
		who    authPrincipal
		course id.ID
		access fakeAccess
		want   error
	}{
		{"bad kind", app.UploadInput{Kind: "audio", ContentType: "audio/mp3", Filename: "a", SizeBytes: 1}, owner, courseA, fakeAccess{}, app.ErrInvalidInput},
		{"kind/type mismatch", app.UploadInput{Kind: "image", ContentType: "video/mp4", Filename: "a", SizeBytes: 1}, owner, courseA, fakeAccess{}, app.ErrInvalidInput},
		{"blank filename", app.UploadInput{Kind: "image", ContentType: "image/png", Filename: " ", SizeBytes: 1}, owner, courseA, fakeAccess{}, app.ErrInvalidInput},
		{"zero size", app.UploadInput{Kind: "image", ContentType: "image/png", Filename: "a", SizeBytes: 0}, owner, courseA, fakeAccess{}, app.ErrInvalidInput},
		{"not manager", valid, stranger, courseA, fakeAccess{}, app.ErrForbidden},
		{"unknown course", valid, owner, 999, fakeAccess{}, app.ErrNotFound},
		{"archived", valid, owner, courseA, fakeAccess{archived: true}, app.ErrCourseNotEditable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, remote := newRepo(), &fakeRemote{}
			_, err := newService(t, repo, remote, tc.access).CreateUpload(ctx, tc.who, tc.course, tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(remote.calls) != 0 || len(repo.rows) != 0 {
				t.Fatalf("side effects: calls=%v rows=%v", remote.calls, repo.rows)
			}
		})
	}
}

func TestCreateUploadRemoteFailureInsertsNothing(t *testing.T) {
	repo, remote := newRepo(), &fakeRemote{err: app.ErrRemoteTooLarge}
	_, err := newService(t, repo, remote, fakeAccess{}).CreateUpload(ctx, owner, courseA,
		app.UploadInput{Kind: "video", ContentType: "video/mp4", Filename: "a.mp4", SizeBytes: 9})
	if !errors.Is(err, app.ErrRemoteTooLarge) || len(repo.rows) != 0 {
		t.Fatalf("err = %v, rows = %v", err, repo.rows)
	}
}

func TestManagedRoutes(t *testing.T) {
	repo := newRepo(videoA)
	remote := &fakeRemote{asset: app.RemoteAsset{ID: videoA.ID, Status: "processing", ProgressPercent: 30}}
	svc := newService(t, repo, remote, fakeAccess{})
	if parts, err := svc.PresignParts(ctx, owner, videoA.ID, []int{3}); err != nil || parts[0].PartNumber != 3 {
		t.Fatalf("parts = %+v, %v", parts, err)
	}
	v, err := svc.Complete(ctx, owner, videoA.ID, []app.CompletedPart{{PartNumber: 1, ETag: "e"}})
	if err != nil || v.Asset != videoA || v.Remote.Status != "processing" {
		t.Fatalf("complete = %+v, %v", v, err)
	}
	if v, err = svc.Status(ctx, owner, videoA.ID); err != nil || v.Remote.ProgressPercent != 30 {
		t.Fatalf("status = %+v, %v", v, err)
	}
	if err := svc.Delete(ctx, owner, videoA.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.rows[videoA.ID]; ok {
		t.Fatal("row survived delete")
	}
	if !slices.Equal(remote.calls, []string{"parts", "complete", "get", "delete"}) {
		t.Fatalf("calls = %v", remote.calls)
	}
}

func TestPresignPartsValidatesNumbers(t *testing.T) {
	svc := newService(t, newRepo(videoA), &fakeRemote{}, fakeAccess{})
	for _, n := range [][]int{nil, {0}, {10001}, make([]int, 1001)} {
		if _, err := svc.PresignParts(ctx, owner, videoA.ID, n); !errors.Is(err, app.ErrInvalidInput) {
			t.Fatalf("%v: err = %v", n, err)
		}
	}
}

func TestManagedRoutesHideAssetsFromNonManagers(t *testing.T) {
	for _, who := range []authPrincipal{stranger, student} {
		remote := &fakeRemote{}
		svc := newService(t, newRepo(videoA), remote, fakeAccess{})
		if _, err := svc.Status(ctx, who, videoA.ID); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("status err = %v", err)
		}
		if err := svc.Delete(ctx, who, videoA.ID); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("delete err = %v", err)
		}
		if _, err := svc.Complete(ctx, who, videoA.ID, nil); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("complete err = %v", err)
		}
		if len(remote.calls) != 0 {
			t.Fatalf("remote called: %v", remote.calls)
		}
	}
	if _, err := newService(t, newRepo(), &fakeRemote{}, fakeAccess{}).Status(ctx, owner, 404); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestDeleteToleratesRemoteNotFound(t *testing.T) {
	repo := newRepo(videoA)
	err := newService(t, repo, &fakeRemote{deleteErr: app.ErrRemoteNotFound}, fakeAccess{}).Delete(ctx, owner, videoA.ID)
	if err != nil || len(repo.rows) != 0 {
		t.Fatalf("err = %v, rows = %v", err, repo.rows)
	}
	repo = newRepo(videoA)
	err = newService(t, repo, &fakeRemote{deleteErr: app.ErrRemoteUnavailable}, fakeAccess{}).Delete(ctx, owner, videoA.ID)
	if !errors.Is(err, app.ErrRemoteUnavailable) || len(repo.rows) != 1 {
		t.Fatalf("unavailable: err = %v, rows = %v", err, repo.rows)
	}
}

func readable(pairs ...[2]id.ID) fakeAccess {
	m := map[[2]id.ID]error{}
	for _, p := range pairs {
		m[p] = nil
	}
	return fakeAccess{read: m}
}

func TestResolveNotReady(t *testing.T) {
	remote := &fakeRemote{asset: app.RemoteAsset{ID: videoA.ID, Status: "processing"}}
	pb, err := newService(t, newRepo(videoA), remote, readable([2]id.ID{lecture1, videoA.ID})).Resolve(ctx, student, courseA, lecture1, videoA.ID)
	if err != nil || pb.Status != "processing" || pb.URL != "" || pb.Kind != domain.KindVideo {
		t.Fatalf("playback = %+v, %v", pb, err)
	}
	if slices.Contains(remote.calls, "delivery") {
		t.Fatal("delivery requested for an asset that is not ready")
	}
}

func TestResolveReadyVideo(t *testing.T) {
	remote := &fakeRemote{
		asset: app.RemoteAsset{ID: videoA.ID, Status: app.StatusReady, DurationMs: 60000, Width: 1280, Height: 720},
		delivery: app.Delivery{URL: "https://media.test/v1/delivery/900/master.m3u8?token=t", ExpiresAt: t0,
			Renditions: []app.Rendition{{Name: "poster", URL: "https://objects.test/poster.jpg"}}},
	}
	pb, err := newService(t, newRepo(videoA), remote, readable([2]id.ID{lecture1, videoA.ID})).Resolve(ctx, student, courseA, lecture1, videoA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pb.URL != remote.delivery.URL || pb.PosterURL != "https://objects.test/poster.jpg" || pb.DurationMs != 60000 ||
		pb.Width != 1280 || pb.Height != 720 || !pb.ExpiresAt.Equal(t0) || pb.Status != app.StatusReady {
		t.Fatalf("playback = %+v", pb)
	}
}

func TestResolveReadyImage(t *testing.T) {
	remote := &fakeRemote{
		asset: app.RemoteAsset{ID: imageA.ID, Status: app.StatusReady, Width: 4000, Height: 3000},
		delivery: app.Delivery{ExpiresAt: t0, Renditions: []app.Rendition{
			{Name: "original-ish", URL: "https://objects.test/x"},
			{Name: app.DisplayVariant, URL: "https://objects.test/display.webp", Width: 1600, Height: 1200}}},
	}
	pb, err := newService(t, newRepo(imageA), remote, readable([2]id.ID{lecture1, imageA.ID})).Resolve(ctx, student, courseA, lecture1, imageA.ID)
	if err != nil || pb.URL != "https://objects.test/display.webp" || pb.Width != 1600 || pb.Height != 1200 || pb.PosterURL != "" {
		t.Fatalf("playback = %+v, %v", pb, err)
	}
}

func TestResolveMissingRendition(t *testing.T) {
	video := &fakeRemote{asset: app.RemoteAsset{Status: app.StatusReady}, delivery: app.Delivery{}}
	if _, err := newService(t, newRepo(videoA), video, readable([2]id.ID{lecture1, videoA.ID})).Resolve(ctx, student, courseA, lecture1, videoA.ID); !errors.Is(err, app.ErrRemoteUnavailable) {
		t.Fatalf("video err = %v", err)
	}
	image := &fakeRemote{asset: app.RemoteAsset{Status: app.StatusReady}, delivery: app.Delivery{Renditions: []app.Rendition{{Name: "poster", URL: "u"}}}}
	if _, err := newService(t, newRepo(imageA), image, readable([2]id.ID{lecture1, imageA.ID})).Resolve(ctx, student, courseA, lecture1, imageA.ID); !errors.Is(err, app.ErrRemoteUnavailable) {
		t.Fatalf("image err = %v", err)
	}
}

func TestResolveRejectsAssetOfAnotherCourse(t *testing.T) {
	remote := &fakeRemote{asset: app.RemoteAsset{Status: app.StatusReady}}
	// Even if the gate would allow it, an asset of course B is never served through course A.
	svc := newService(t, newRepo(videoB), remote, readable([2]id.ID{lecture1, videoB.ID}))
	if _, err := svc.Resolve(ctx, student, courseA, lecture1, videoB.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(remote.calls) != 0 {
		t.Fatalf("remote called: %v", remote.calls)
	}
}

func TestResolveGate(t *testing.T) {
	gate := fakeAccess{read: map[[2]id.ID]error{{lecture1, videoA.ID}: app.ErrEnrollmentRequired}}
	if _, err := newService(t, newRepo(videoA), &fakeRemote{}, gate).Resolve(ctx, student, courseA, lecture1, videoA.ID); !errors.Is(err, app.ErrEnrollmentRequired) {
		t.Fatalf("gate err = %v", err)
	}
	if _, err := newService(t, newRepo(videoA), &fakeRemote{}, fakeAccess{}).Resolve(ctx, student, courseA, lecture1, videoA.ID); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unreferenced err = %v", err)
	}
	if _, err := newService(t, newRepo(), &fakeRemote{}, fakeAccess{}).Resolve(ctx, student, courseA, lecture1, 404); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestAssetQueryKinds(t *testing.T) {
	kinds, err := app.NewAssetQuery(newRepo(videoA, imageA, videoB)).Kinds(ctx, courseA, []id.ID{videoA.ID, imageA.ID, videoB.ID})
	if err != nil || len(kinds) != 2 || kinds[videoA.ID] != domain.KindVideo || kinds[imageA.ID] != domain.KindImage {
		t.Fatalf("kinds = %v, %v", kinds, err)
	}
}

func TestDeleteRefusesAssetInUse(t *testing.T) {
	repo, remote := newRepo(videoA), &fakeRemote{}
	svc := newService(t, repo, remote, fakeAccess{usage: map[id.ID][]id.ID{videoA.ID: {50, 51}}})
	err := svc.Delete(ctx, owner, videoA.ID)
	var inUse *app.InUseError
	if !errors.As(err, &inUse) || !errors.Is(err, app.ErrAssetInUse) || !slices.Equal(inUse.LectureIDs, []id.ID{50, 51}) {
		t.Fatalf("delete = %v", err)
	}
	if len(remote.calls) != 0 {
		t.Fatalf("remote touched: %v", remote.calls)
	}
}
