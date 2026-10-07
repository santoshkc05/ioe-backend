package app_test

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

// memStore mimics the postgres repositories. memStore itself is the quiz repository; memExams
// is the exam repository over the same data.
type memStore struct {
	mu           sync.Mutex
	quizzes      map[id.ID]domain.Quiz
	quizRevs     map[id.ID][]domain.Quiz
	quizDeleted  map[id.ID]bool
	attempts     []domain.QuizAttempt
	exams        map[id.ID]domain.Exam
	examRevs     map[id.ID][]domain.Exam
	examDeleted  map[id.ID]bool
	examAttempts map[id.ID]domain.ExamAttempt
	locks        []app.LockMode // every lock mode passed to Exams.Find, in order
}

func newMemStore() *memStore {
	return &memStore{
		quizzes:      map[id.ID]domain.Quiz{},
		quizRevs:     map[id.ID][]domain.Quiz{},
		quizDeleted:  map[id.ID]bool{},
		exams:        map[id.ID]domain.Exam{},
		examRevs:     map[id.ID][]domain.Exam{},
		examDeleted:  map[id.ID]bool{},
		examAttempts: map[id.ID]domain.ExamAttempt{},
	}
}

func cloneQuizRevs(m map[id.ID][]domain.Quiz) map[id.ID][]domain.Quiz {
	out := make(map[id.ID][]domain.Quiz, len(m))
	for k, v := range m {
		out[k] = slices.Clone(v)
	}
	return out
}

func cloneExamRevs(m map[id.ID][]domain.Exam) map[id.ID][]domain.Exam {
	out := make(map[id.ID][]domain.Exam, len(m))
	for k, v := range m {
		out[k] = slices.Clone(v)
	}
	return out
}

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Run on copies so a failed transaction leaves no trace, as a rollback would.
	quizzes, qRevs, qDel := maps.Clone(m.quizzes), cloneQuizRevs(m.quizRevs), maps.Clone(m.quizDeleted)
	exams, eRevs, eDel := maps.Clone(m.exams), cloneExamRevs(m.examRevs), maps.Clone(m.examDeleted)
	attempts, eAttempts := slices.Clone(m.attempts), maps.Clone(m.examAttempts)
	if err := fn(app.Repos{Quizzes: m, Exams: memExams{m}}); err != nil {
		m.quizzes, m.quizRevs, m.quizDeleted = quizzes, qRevs, qDel
		m.exams, m.examRevs, m.examDeleted = exams, eRevs, eDel
		m.attempts, m.examAttempts = attempts, eAttempts
		return err
	}
	return nil
}

func (m *memStore) Find(_ context.Context, quizID id.ID) (domain.Quiz, error) {
	if m.quizDeleted[quizID] {
		return domain.Quiz{}, app.ErrNotFound
	}
	revs, ok := m.quizRevs[quizID]
	if !ok || len(revs) == 0 {
		return domain.Quiz{}, app.ErrNotFound
	}
	return revs[len(revs)-1], nil
}

func (m *memStore) FindForUpdate(ctx context.Context, quizID id.ID) (domain.Quiz, error) {
	return m.Find(ctx, quizID)
}

func (m *memStore) FindRevisions(_ context.Context, revs map[id.ID]int) ([]domain.Quiz, error) {
	var out []domain.Quiz
	for qid, rev := range revs {
		list, ok := m.quizRevs[qid]
		if !ok || rev <= 0 || rev > len(list) {
			continue
		}
		out = append(out, list[rev-1])
	}
	slices.SortFunc(out, func(a, b domain.Quiz) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (m *memStore) ListByLecture(_ context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error) {
	var out []domain.Quiz
	for qid, revs := range m.quizRevs {
		if m.quizDeleted[qid] || len(revs) == 0 {
			continue
		}
		head := revs[len(revs)-1]
		if head.CourseID == courseID && head.LectureID == lectureID {
			out = append(out, head)
		}
	}
	slices.SortFunc(out, func(a, b domain.Quiz) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (m *memStore) Insert(_ context.Context, q domain.Quiz, _ id.ID) error {
	if q.Revision != 1 {
		return fmt.Errorf("revision must be 1")
	}
	m.quizRevs[q.ID] = []domain.Quiz{q}
	m.quizzes[q.ID] = q
	delete(m.quizDeleted, q.ID)
	return nil
}

func (m *memStore) AppendRevision(_ context.Context, q domain.Quiz, _ id.ID) error {
	if m.quizDeleted[q.ID] {
		return app.ErrNotFound
	}
	revs, ok := m.quizRevs[q.ID]
	if !ok || len(revs) != q.Revision-1 {
		return app.ErrNotFound
	}
	m.quizRevs[q.ID] = append(revs, q)
	m.quizzes[q.ID] = q
	return nil
}

func (m *memStore) Delete(_ context.Context, quizID id.ID, _ time.Time) error {
	m.quizDeleted[quizID] = true
	delete(m.quizzes, quizID)
	return nil
}

func (m *memStore) Undelete(_ context.Context, quizID id.ID, _ time.Time) error {
	m.quizDeleted[quizID] = false
	return nil
}

func (m *memStore) Heads(_ context.Context, courseID id.ID) ([]app.Head, error) {
	var out []app.Head
	for qid, revs := range m.quizRevs {
		if len(revs) == 0 || revs[0].CourseID != courseID {
			continue
		}
		out = append(out, app.Head{
			Ref:      app.Ref{Kind: app.KindQuiz, ID: qid},
			Revision: revs[len(revs)-1].Revision,
			Deleted:  m.quizDeleted[qid],
		})
	}
	slices.SortFunc(out, func(a, b app.Head) int { return cmp.Compare(a.Ref.ID, b.Ref.ID) })
	return out, nil
}

func (m *memStore) LecturesOf(_ context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error) {
	out := map[id.ID]id.ID{}
	for _, v := range quizIDs {
		if revs, ok := m.quizRevs[v]; ok && len(revs) > 0 && revs[0].CourseID == courseID && !m.quizDeleted[v] {
			out[v] = revs[0].LectureID
		}
	}
	return out, nil
}

func (m *memStore) RecordAttempt(_ context.Context, a domain.QuizAttempt) (id.ID, error) {
	if a.IdempotencyKey != "" {
		for _, prev := range m.attempts {
			if prev.QuizID == a.QuizID && prev.UserID == a.UserID && prev.IdempotencyKey == a.IdempotencyKey {
				return prev.ID, nil
			}
		}
	}
	m.attempts = append(m.attempts, a)
	return a.ID, nil
}

// outsideTx fails the test when a cross-context port is called inside a store transaction.
func outsideTx(t *testing.T, s *memStore) {
	t.Helper()
	if !s.mu.TryLock() {
		t.Error("cross-context port called inside the assessment transaction")
		return
	}
	s.mu.Unlock()
}

// courseAccess applies courseauthoring's rules to a fixed course layout.
type courseAccess struct {
	t        *testing.T
	store    *memStore
	owner    id.ID
	lectures map[id.ID]id.ID // lecture → course
	archived map[id.ID]bool  // course → archived
	hidden   map[id.ID]bool  // course → invisible to non-managers (draft or archived)
	free     map[id.ID]bool  // free-preview lectures
	enrolled enrollments
	live     map[id.ID]app.Pins
	versions map[[2]int64]app.Pins
	edits    []id.ID
	frozen   map[id.ID]bool
}

func (a *courseAccess) BeginEdit(_ context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	outsideTx(a.t, a.store)
	switch {
	case !a.known(courseID), !lectureID.IsZero() && a.lectures[lectureID] != courseID:
		return app.ErrNotFound
	case !a.manages(p):
		return app.ErrForbidden
	case a.archived[courseID], a.frozen[courseID]:
		return app.ErrCourseNotEditable
	}
	a.edits = append(a.edits, courseID)
	return nil
}

func (a *courseAccess) LivePins(_ context.Context, courseID id.ID) (app.Pins, bool, error) {
	outsideTx(a.t, a.store)
	pins, ok := a.live[courseID]
	return maps.Clone(pins), ok, nil
}

func (a *courseAccess) VersionPins(ctx context.Context, p auth.Principal, courseID id.ID, number int) (app.Pins, error) {
	if err := a.CanReadAsManager(ctx, p, courseID); err != nil {
		return nil, err
	}
	pins, ok := a.versions[[2]int64{int64(courseID), int64(number)}]
	if !ok {
		return nil, app.ErrNotFound
	}
	return maps.Clone(pins), nil
}

func (a *courseAccess) known(courseID id.ID) bool {
	for _, c := range a.lectures {
		if c == courseID {
			return true
		}
	}
	return false
}

func (a *courseAccess) manages(p auth.Principal) bool {
	return p.UserID == a.owner || p.Role == auth.RoleRootAdmin
}

func (a *courseAccess) CanReadAsManager(ctx context.Context, p auth.Principal, courseID id.ID) error {
	if err := a.CanReadCourse(ctx, p, courseID); err != nil {
		return err
	}
	if !a.manages(p) {
		return app.ErrForbidden
	}
	return nil
}

func (a *courseAccess) CanReadCourse(_ context.Context, p auth.Principal, courseID id.ID) error {
	outsideTx(a.t, a.store)
	if !a.known(courseID) || (a.hidden[courseID] && !a.manages(p)) {
		return app.ErrNotFound
	}
	return nil
}

func (a *courseAccess) CanReadLecture(_ context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	outsideTx(a.t, a.store)
	switch {
	case a.lectures[lectureID] != courseID:
		return app.ErrNotFound
	case p.UserID == a.owner || p.Role == auth.RoleRootAdmin || a.free[lectureID] || a.enrolled[[2]id.ID{courseID, p.UserID}]:
		return nil
	}
	return app.ErrEnrollmentRequired
}

// enrollments reports {course, user} pairs as actively enrolled.
type enrollments map[[2]id.ID]bool

type enrollmentProbe struct {
	t     *testing.T
	store *memStore
	set   enrollments
}

func (e enrollmentProbe) IsActivelyEnrolled(_ context.Context, courseID, userID id.ID) (bool, error) {
	outsideTx(e.t, e.store)
	return e.set[[2]id.ID{courseID, userID}], nil
}

type memExams struct{ m *memStore }

func (r memExams) Find(_ context.Context, examID id.ID, lock app.LockMode) (domain.Exam, error) {
	r.m.locks = append(r.m.locks, lock)
	if r.m.examDeleted[examID] {
		return domain.Exam{}, app.ErrNotFound
	}
	revs, ok := r.m.examRevs[examID]
	if !ok || len(revs) == 0 {
		return domain.Exam{}, app.ErrNotFound
	}
	return revs[len(revs)-1], nil
}

func (r memExams) FindRevisions(_ context.Context, revs map[id.ID]int) ([]domain.Exam, error) {
	var out []domain.Exam
	for eid, rev := range revs {
		list, ok := r.m.examRevs[eid]
		if !ok || rev <= 0 || rev > len(list) {
			continue
		}
		out = append(out, list[rev-1])
	}
	slices.SortFunc(out, func(a, b domain.Exam) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r memExams) ListByCourse(_ context.Context, courseID id.ID) ([]domain.Exam, error) {
	var out []domain.Exam
	for eid, revs := range r.m.examRevs {
		if r.m.examDeleted[eid] || len(revs) == 0 {
			continue
		}
		head := revs[len(revs)-1]
		if head.CourseID == courseID {
			out = append(out, head)
		}
	}
	slices.SortFunc(out, func(a, b domain.Exam) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r memExams) Insert(_ context.Context, e domain.Exam, _ id.ID) error {
	if e.Revision != 1 {
		return fmt.Errorf("revision must be 1")
	}
	r.m.examRevs[e.ID] = []domain.Exam{e}
	r.m.exams[e.ID] = e
	delete(r.m.examDeleted, e.ID)
	return nil
}

func (r memExams) AppendRevision(_ context.Context, e domain.Exam, _ id.ID) error {
	if r.m.examDeleted[e.ID] {
		return app.ErrNotFound
	}
	revs, ok := r.m.examRevs[e.ID]
	if !ok || len(revs) != e.Revision-1 {
		return app.ErrNotFound
	}
	r.m.examRevs[e.ID] = append(revs, e)
	r.m.exams[e.ID] = e
	return nil
}

func (r memExams) Delete(_ context.Context, examID id.ID, _ time.Time) error {
	r.m.examDeleted[examID] = true
	delete(r.m.exams, examID)
	return nil
}

func (r memExams) Undelete(_ context.Context, examID id.ID, _ time.Time) error {
	r.m.examDeleted[examID] = false
	return nil
}

func (r memExams) Heads(_ context.Context, courseID id.ID) ([]app.Head, error) {
	var out []app.Head
	for eid, revs := range r.m.examRevs {
		if len(revs) == 0 || revs[0].CourseID != courseID {
			continue
		}
		out = append(out, app.Head{
			Ref:      app.Ref{Kind: app.KindExam, ID: eid},
			Revision: revs[len(revs)-1].Revision,
			Deleted:  r.m.examDeleted[eid],
		})
	}
	slices.SortFunc(out, func(a, b app.Head) int { return cmp.Compare(a.Ref.ID, b.Ref.ID) })
	return out, nil
}

func (r memExams) NextPosition(_ context.Context, courseID id.ID) (int, error) {
	next := 0
	for eid, revs := range r.m.examRevs {
		if r.m.examDeleted[eid] || len(revs) == 0 {
			continue
		}
		head := revs[len(revs)-1]
		if head.CourseID == courseID && head.Position >= next {
			next = head.Position + 1
		}
	}
	return next, nil
}

func (r memExams) FindAttempt(_ context.Context, attemptID id.ID, _ bool) (domain.ExamAttempt, error) {
	a, ok := r.m.examAttempts[attemptID]
	if !ok {
		return domain.ExamAttempt{}, app.ErrNotFound
	}
	return a, nil
}

func (r memExams) FindOpenAttempt(_ context.Context, examID, userID id.ID) (domain.ExamAttempt, error) {
	for _, a := range r.m.examAttempts {
		if a.ExamID == examID && a.UserID == userID && a.Open() {
			return a, nil
		}
	}
	return domain.ExamAttempt{}, app.ErrNotFound
}

func (r memExams) HasSubmitted(_ context.Context, examID, userID id.ID) (bool, error) {
	for _, a := range r.m.examAttempts {
		if a.ExamID == examID && a.UserID == userID && !a.Open() {
			return true, nil
		}
	}
	return false, nil
}

func (r memExams) InsertAttempt(ctx context.Context, a domain.ExamAttempt) error {
	if _, err := r.FindOpenAttempt(ctx, a.ExamID, a.UserID); err == nil {
		return app.ErrOpenAttemptExists
	}
	r.m.examAttempts[a.ID] = a
	return nil
}

func (r memExams) MergeAnswer(_ context.Context, attemptID id.ID, ans domain.ExamAnswer) error {
	a, ok := r.m.examAttempts[attemptID]
	if !ok || !a.Open() {
		return domain.ErrAttemptSubmitted
	}
	a.Answers = slices.DeleteFunc(slices.Clone(a.Answers), func(x domain.ExamAnswer) bool { return x.QuestionID == ans.QuestionID })
	a.Answers = append(a.Answers, ans)
	r.m.examAttempts[attemptID] = a
	return nil
}

func (r memExams) SaveResult(_ context.Context, a domain.ExamAttempt) error {
	if cur, ok := r.m.examAttempts[a.ID]; !ok || !cur.Open() {
		return domain.ErrAttemptSubmitted
	}
	r.m.examAttempts[a.ID] = a
	return nil
}

func (r memExams) ListAttempts(_ context.Context, examID id.ID) ([]domain.ExamAttempt, error) {
	return r.attempts(func(a domain.ExamAttempt) bool { return a.ExamID == examID }), nil
}

func (r memExams) ListUserAttempts(_ context.Context, courseID, userID id.ID) ([]domain.ExamAttempt, error) {
	return r.attempts(func(a domain.ExamAttempt) bool { return a.CourseID == courseID && a.UserID == userID }), nil
}

func (r memExams) attempts(keep func(domain.ExamAttempt) bool) []domain.ExamAttempt {
	var out []domain.ExamAttempt
	for _, a := range r.m.examAttempts {
		if keep(a) {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b domain.ExamAttempt) int {
		return cmp.Or(a.StartedAt.Compare(b.StartedAt), cmp.Compare(a.ID, b.ID))
	})
	return out
}
