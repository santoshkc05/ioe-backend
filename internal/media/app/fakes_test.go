package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/media/app"
	"github.com/santoshkc2200/ioe-backend/internal/media/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

var (
	ctx      = context.Background()
	t0       = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	owner    = auth.Principal{UserID: 100, Role: auth.RoleInstructor}
	stranger = auth.Principal{UserID: 101, Role: auth.RoleInstructor}
	student  = auth.Principal{UserID: 200, Role: auth.RoleStudent}
)

type authPrincipal = auth.Principal

const (
	courseA  id.ID = 10
	courseB  id.ID = 11
	lecture1 id.ID = 20
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return t0 }

type memRepo struct {
	rows      map[id.ID]domain.Asset
	insertErr error
}

func newRepo(rows ...domain.Asset) *memRepo {
	r := &memRepo{rows: map[id.ID]domain.Asset{}}
	for _, a := range rows {
		r.rows[a.ID] = a
	}
	return r
}

func (r *memRepo) Insert(_ context.Context, a domain.Asset) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	r.rows[a.ID] = a
	return nil
}

func (r *memRepo) Find(_ context.Context, assetID id.ID) (domain.Asset, error) {
	a, ok := r.rows[assetID]
	if !ok {
		return domain.Asset{}, app.ErrNotFound
	}
	return a, nil
}

func (r *memRepo) Delete(_ context.Context, assetID id.ID) error {
	delete(r.rows, assetID)
	return nil
}

func (r *memRepo) KindsInCourse(_ context.Context, courseID id.ID, ids []id.ID) (map[id.ID]domain.Kind, error) {
	out := map[id.ID]domain.Kind{}
	for _, v := range ids {
		if a, ok := r.rows[v]; ok && a.CourseID == courseID {
			out[v] = a.Kind
		}
	}
	return out, nil
}

// fakeRemote records calls and returns canned values.
type fakeRemote struct {
	created   []app.RemoteCreate
	upload    app.Upload
	asset     app.RemoteAsset
	delivery  app.Delivery
	err       error
	deleteErr error
	calls     []string
}

func (f *fakeRemote) Create(_ context.Context, in app.RemoteCreate) (app.Upload, error) {
	f.calls = append(f.calls, "create")
	f.created = append(f.created, in)
	return f.upload, f.err
}

func (f *fakeRemote) PresignParts(_ context.Context, _ id.ID, n []int) ([]app.UploadPart, error) {
	f.calls = append(f.calls, "parts")
	return []app.UploadPart{{PartNumber: n[0], URL: "https://objects.test/p"}}, f.err
}

func (f *fakeRemote) Complete(context.Context, id.ID, []app.CompletedPart) (app.RemoteAsset, error) {
	f.calls = append(f.calls, "complete")
	return f.asset, f.err
}

func (f *fakeRemote) Get(context.Context, id.ID) (app.RemoteAsset, error) {
	f.calls = append(f.calls, "get")
	return f.asset, f.err
}

func (f *fakeRemote) Delete(context.Context, id.ID) error {
	f.calls = append(f.calls, "delete")
	return f.deleteErr
}

func (f *fakeRemote) Delivery(context.Context, id.ID) (app.Delivery, error) {
	f.calls = append(f.calls, "delivery")
	return f.delivery, f.err
}

// fakeAccess: owner manages courseA and courseB; readable maps (lecture, asset) to the gate result.
type fakeAccess struct {
	archived bool
	read     map[[2]id.ID]error
	usage    map[id.ID][]id.ID
}

func (f fakeAccess) CanManage(_ context.Context, p auth.Principal, courseID id.ID) error {
	if courseID != courseA && courseID != courseB {
		return app.ErrNotFound
	}
	if p.UserID != owner.UserID {
		return app.ErrForbidden
	}
	if f.archived {
		return app.ErrCourseNotEditable
	}
	return nil
}

func (f fakeAccess) CanReadLectureAsset(_ context.Context, _ auth.Principal, _, lectureID, assetID id.ID) error {
	if err, ok := f.read[[2]id.ID{lectureID, assetID}]; ok {
		return err
	}
	return app.ErrNotFound
}

func (f fakeAccess) AssetUsage(_ context.Context, _, assetID id.ID) ([]id.ID, error) {
	return f.usage[assetID], nil
}

func testIDs(t *testing.T) *id.Generator {
	t.Helper()
	g, err := id.NewGenerator(1)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func newService(t *testing.T, repo *memRepo, remote *fakeRemote, access fakeAccess) *app.AssetService {
	t.Helper()
	return app.NewAssetService(repo, remote, access, testIDs(t), fixedClock{})
}
