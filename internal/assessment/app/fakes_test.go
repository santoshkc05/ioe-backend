package app_test

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sort"
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
	attempts     []domain.QuizAttempt
	exams        map[id.ID]domain.Exam
	examAttempts map[id.ID]domain.ExamAttempt
	locks        []app.LockMode // every lock mode passed to Exams.Find, in order
}

func newMemStore() *memStore {
	return &memStore{quizzes: map[id.ID]domain.Quiz{}, exams: map[id.ID]domain.Exam{}, examAttempts: map[id.ID]domain.ExamAttempt{}}
}

func (m *memStore) RunInTx(_ context.Context, fn func(app.Repos) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Run on copies so a failed transaction leaves no trace, as a rollback would.
	exams, attempts := maps.Clone(m.exams), maps.Clone(m.examAttempts)
	if err := fn(app.Repos{Quizzes: m, Exams: memExams{m}}); err != nil {
		m.exams, m.examAttempts = exams, attempts
		return err
	}
	return nil
}

func (m *memStore) Find(_ context.Context, quizID id.ID) (domain.Quiz, error) {
	q, ok := m.quizzes[quizID]
	if !ok {
		return domain.Quiz{}, app.ErrNotFound
	}
	return q, nil
}

func (m *memStore) ListByLecture(_ context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error) {
	var out []domain.Quiz
	for _, q := range m.quizzes {
		if q.CourseID == courseID && q.LectureID == lectureID {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *memStore) Insert(_ context.Context, q domain.Quiz) error {
	m.quizzes[q.ID] = q
	return nil
}

func (m *memStore) Replace(_ context.Context, q domain.Quiz) error {
	if _, ok := m.quizzes[q.ID]; !ok {
		return app.ErrNotFound
	}
	m.quizzes[q.ID] = q
	return nil
}

func (m *memStore) Delete(_ context.Context, quizID id.ID) error {
	delete(m.quizzes, quizID)
	kept := m.attempts[:0]
	for _, a := range m.attempts {
		if a.QuizID != quizID {
			kept = append(kept, a)
		}
	}
	m.attempts = kept
	return nil
}

func (m *memStore) LecturesOf(_ context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error) {
	out := map[id.ID]id.ID{}
	for _, v := range quizIDs {
		if q, ok := m.quizzes[v]; ok && q.CourseID == courseID {
			out[v] = q.LectureID
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

func (a *courseAccess) CanManageCourse(ctx context.Context, p auth.Principal, courseID id.ID) error {
	if err := a.CanReadAsManager(ctx, p, courseID); err != nil {
		return err
	}
	if a.archived[courseID] {
		return app.ErrCourseNotEditable
	}
	return nil
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

func (a *courseAccess) CanManageLecture(_ context.Context, p auth.Principal, courseID, lectureID id.ID) error {
	outsideTx(a.t, a.store)
	switch {
	case a.lectures[lectureID] != courseID:
		return app.ErrNotFound
	case p.UserID != a.owner && p.Role != auth.RoleRootAdmin:
		return app.ErrForbidden
	case a.archived[courseID]:
		return app.ErrCourseNotEditable
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
	e, ok := r.m.exams[examID]
	if !ok {
		return domain.Exam{}, app.ErrNotFound
	}
	return e, nil
}

func (r memExams) ListByCourse(_ context.Context, courseID id.ID, publishedOnly bool) ([]domain.Exam, error) {
	var out []domain.Exam
	for _, e := range r.m.exams {
		if e.CourseID == courseID && (!publishedOnly || e.Status == domain.ExamPublished) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b domain.Exam) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r memExams) Insert(_ context.Context, e domain.Exam) error {
	r.m.exams[e.ID] = e
	return nil
}

func (r memExams) Replace(_ context.Context, e domain.Exam) error {
	if _, ok := r.m.exams[e.ID]; !ok {
		return app.ErrNotFound
	}
	r.m.exams[e.ID] = e
	return nil
}

func (r memExams) Delete(_ context.Context, examID id.ID) error {
	for _, a := range r.m.examAttempts {
		if a.ExamID == examID {
			return app.ErrExamHasAttempts
		}
	}
	delete(r.m.exams, examID)
	return nil
}

func (r memExams) NextPosition(_ context.Context, courseID id.ID) (int, error) {
	next := 0
	for _, e := range r.m.exams {
		if e.CourseID == courseID && e.Position >= next {
			next = e.Position + 1
		}
	}
	return next, nil
}

func (r memExams) SetPositions(_ context.Context, courseID id.ID, examIDs []id.ID) error {
	for i, v := range examIDs {
		if e, ok := r.m.exams[v]; ok && e.CourseID == courseID {
			e.Position = i
			r.m.exams[v] = e
		}
	}
	return nil
}

func (r memExams) Locks(_ context.Context, examID id.ID) (domain.Locks, error) {
	l := domain.Locks{AnsweredQuestionIDs: map[id.ID]struct{}{}}
	for _, a := range r.m.examAttempts {
		if a.ExamID != examID {
			continue
		}
		if a.Open() {
			l.OpenAttempts++
		} else {
			l.SubmittedAttempts++
		}
		for _, ans := range a.Answers {
			if len(ans.OptionIDs) > 0 {
				l.AnsweredQuestionIDs[ans.QuestionID] = struct{}{}
			}
		}
	}
	return l, nil
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
