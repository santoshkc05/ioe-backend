package app_test

import (
	"testing"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
)

func TestRestorePins(t *testing.T) {
	f := newFixture(t)
	restore := app.NewRestoreService(f.store, f.clock)
	kept, _ := f.svc.Create(ctx, owner, course, locked, quizInput(0, "v1"))
	gone, _ := f.svc.Create(ctx, owner, course, locked, quizInput(1, "gone"))
	f.goLive(t, course)
	pins := f.access.live[course]

	_, _ = f.svc.Update(ctx, owner, course, locked, kept.ID, quizInput(0, "v2"))
	_ = f.svc.Delete(ctx, owner, gone.ID)
	added, _ := f.svc.Create(ctx, owner, course, locked, quizInput(2, "new"))

	for range 2 { // redelivery must change nothing
		if err := restore.RestorePins(ctx, course, owner.UserID, pins); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := f.svc.List(ctx, owner, course, locked, 0)
	if len(got) != 2 || got[0].ID != kept.ID || got[1].ID != gone.ID {
		t.Fatalf("working copy = %+v", got)
	}
	if got[0].Questions[0].Prompt != "v1" || got[0].Revision != 3 {
		t.Fatalf("kept = rev %d %q, want rev 3 v1", got[0].Revision, got[0].Questions[0].Prompt)
	}
	if got[1].Revision != 1 {
		t.Fatalf("undeleted quiz moved to rev %d, want 1", got[1].Revision)
	}
	if _, err := f.svc.Update(ctx, owner, course, locked, added.ID, quizInput(2, "x")); err == nil {
		t.Fatal("quiz created after the live version was not deleted")
	}
}
