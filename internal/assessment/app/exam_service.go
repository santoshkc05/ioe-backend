package app

import (
	"context"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/clock"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// ExamSettings are the exam fields an instructor sets besides position and questions.
type ExamSettings struct {
	Title, Description string
	PassMark           int
	TimeLimitSeconds   *int // nil means untimed
	RetakesAllowed     bool
	OpensAt, ClosesAt  *time.Time
	RevealPolicy       string
}

type ExamInput struct {
	ExamSettings
	Position  int
	Questions []QuestionInput
}

type ExamSettingsInput struct {
	ExamSettings
	Points map[string]int // question ID → points; omitted questions keep theirs
}

// ExamDetail is an exam with the attempt locks on it: the authoring view.
type ExamDetail struct {
	Exam  domain.Exam
	Locks domain.Locks
}

// ExamService implements exam authoring and attempts. Course access and enrollment checks run
// before the assessment transaction opens: each takes its own pool connection.
type ExamService struct {
	tx          TxRunner
	courses     CourseAccess
	enrollments EnrollmentQuery
	ids         *id.Generator
	clock       clock.Clock
}

func NewExamService(tx TxRunner, courses CourseAccess, enrollments EnrollmentQuery, ids *id.Generator, clk clock.Clock) *ExamService {
	return &ExamService{tx: tx, courses: courses, enrollments: enrollments, ids: ids, clock: clk}
}

// ListAuthoring returns every exam of a course the caller manages, drafts included.
func (s *ExamService) ListAuthoring(ctx context.Context, p auth.Principal, courseID id.ID) ([]domain.Exam, error) {
	if err := s.courses.CanReadAsManager(ctx, p, courseID); err != nil {
		return nil, err
	}
	var out []domain.Exam
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		out, err = r.Exams.ListByCourse(ctx, courseID, false)
		return err
	})
	return out, err
}

func (s *ExamService) GetAuthoring(ctx context.Context, p auth.Principal, examID id.ID) (ExamDetail, error) {
	if err := s.authorize(ctx, p, examID, s.courses.CanReadAsManager); err != nil {
		return ExamDetail{}, err
	}
	var out ExamDetail
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		e, err := r.Exams.Find(ctx, examID, LockNone)
		if err != nil {
			return err
		}
		if err := settleExamAttempts(ctx, r, e, s.clock.Now()); err != nil {
			return err
		}
		locks, err := r.Exams.Locks(ctx, examID)
		out = ExamDetail{Exam: e, Locks: locks}
		return err
	})
	return out, err
}

// Create adds a draft exam. Question and option IDs are generated.
func (s *ExamService) Create(ctx context.Context, p auth.Principal, courseID id.ID, in ExamInput) (ExamDetail, error) {
	if err := s.courses.CanManageCourse(ctx, p, courseID); err != nil {
		return ExamDetail{}, err
	}
	now := s.clock.Now()
	e, err := s.buildExam(domain.Exam{ID: s.ids.New(), CourseID: courseID, Status: domain.ExamDraft, CreatedAt: now, UpdatedAt: now}, in, nil)
	if err != nil {
		return ExamDetail{}, err
	}
	if err := s.tx.RunInTx(ctx, func(r Repos) error { return r.Exams.Insert(ctx, e) }); err != nil {
		return ExamDetail{}, err
	}
	return ExamDetail{Exam: e}, nil
}

// Save replaces an exam's settings, position, and questions. Supplied question and option IDs
// must already belong to the exam; status is kept.
func (s *ExamService) Save(ctx context.Context, p auth.Principal, examID id.ID, in ExamInput) (ExamDetail, error) {
	return s.edit(ctx, p, examID, func(cur domain.Exam, now time.Time) (domain.Exam, error) {
		base := domain.Exam{ID: cur.ID, CourseID: cur.CourseID, Status: cur.Status, CreatedAt: cur.CreatedAt, UpdatedAt: now}
		return s.buildExam(base, in, cur.IDs())
	})
}

// SaveSettings replaces an exam's settings and the points of the listed questions.
func (s *ExamService) SaveSettings(ctx context.Context, p auth.Principal, examID id.ID, in ExamSettingsInput) (ExamDetail, error) {
	return s.edit(ctx, p, examID, func(cur domain.Exam, now time.Time) (domain.Exam, error) {
		next := cur
		next.Questions = slices.Clone(cur.Questions)
		for raw, points := range in.Points {
			qid, err := id.Parse(raw)
			if err != nil {
				return domain.Exam{}, fmt.Errorf("%w: points: %w", ErrInvalidInput, err)
			}
			i := slices.IndexFunc(next.Questions, func(q domain.Question) bool { return q.ID == qid })
			switch {
			case i < 0:
				return domain.Exam{}, fmt.Errorf("%w: points: question %s is not in the exam", ErrInvalidInput, raw)
			case points < 1:
				return domain.Exam{}, fmt.Errorf("%w: points: question %s needs at least 1 point", ErrInvalidInput, raw)
			}
			next.Questions[i].Points = points
		}
		if err := in.apply(&next); err != nil {
			return domain.Exam{}, err
		}
		next.UpdatedAt = now
		return validExam(next)
	})
}

// Reorder sets exam positions to their order in examIDs, which must list each exam of the
// course exactly once.
func (s *ExamService) Reorder(ctx context.Context, p auth.Principal, courseID id.ID, examIDs []id.ID) error {
	if err := s.courses.CanManageCourse(ctx, p, courseID); err != nil {
		return err
	}
	return s.tx.RunInTx(ctx, func(r Repos) error {
		exams, err := r.Exams.ListByCourse(ctx, courseID, false)
		if err != nil {
			return err
		}
		want := make(map[id.ID]struct{}, len(exams))
		for _, e := range exams {
			want[e.ID] = struct{}{}
		}
		for _, v := range examIDs {
			if _, ok := want[v]; !ok {
				return fmt.Errorf("%w: exam_ids must list each exam of the course once", ErrInvalidInput)
			}
			delete(want, v)
		}
		if len(want) > 0 {
			return fmt.Errorf("%w: exam_ids must list each exam of the course once", ErrInvalidInput)
		}
		return r.Exams.SetPositions(ctx, courseID, examIDs)
	})
}

func (s *ExamService) Publish(ctx context.Context, p auth.Principal, examID id.ID) error {
	return s.setStatus(ctx, p, examID, domain.ExamPublished)
}

// Unpublish hides the exam and blocks new attempts. Open attempts continue.
func (s *ExamService) Unpublish(ctx context.Context, p auth.Principal, examID id.ID) error {
	return s.setStatus(ctx, p, examID, domain.ExamDraft)
}

func (s *ExamService) setStatus(ctx context.Context, p auth.Principal, examID id.ID, status domain.ExamStatus) error {
	_, err := s.edit(ctx, p, examID, func(cur domain.Exam, now time.Time) (domain.Exam, error) {
		cur.Status, cur.UpdatedAt = status, now
		return cur, nil
	})
	return err
}

// Duplicate copies an exam with fresh IDs as a draft at the end of the course's exams.
func (s *ExamService) Duplicate(ctx context.Context, p auth.Principal, examID id.ID) (ExamDetail, error) {
	if err := s.authorize(ctx, p, examID, s.courses.CanManageCourse); err != nil {
		return ExamDetail{}, err
	}
	var out domain.Exam
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		src, err := r.Exams.Find(ctx, examID, LockNone)
		if err != nil {
			return err
		}
		position, err := r.Exams.NextPosition(ctx, src.CourseID)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		cp := src
		cp.ID, cp.Status, cp.Position, cp.CreatedAt, cp.UpdatedAt = s.ids.New(), domain.ExamDraft, position, now, now
		cp.Title = truncate(src.Title+" (copy)", domain.MaxTitleLen)
		cp.Questions = make([]domain.Question, len(src.Questions))
		for i, q := range src.Questions {
			q.ID = s.ids.New()
			q.Options = slices.Clone(q.Options)
			for j := range q.Options {
				q.Options[j].ID = s.ids.New()
			}
			cp.Questions[i] = q
		}
		if out, err = validExam(cp); err != nil {
			return err
		}
		return r.Exams.Insert(ctx, out)
	})
	return ExamDetail{Exam: out}, err
}

// Delete removes an exam that has no attempts.
func (s *ExamService) Delete(ctx context.Context, p auth.Principal, examID id.ID) error {
	if err := s.authorize(ctx, p, examID, s.courses.CanManageCourse); err != nil {
		return err
	}
	return s.tx.RunInTx(ctx, func(r Repos) error { return r.Exams.Delete(ctx, examID) })
}

func (s *ExamService) find(ctx context.Context, examID id.ID) (domain.Exam, error) {
	var e domain.Exam
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		var err error
		e, err = r.Exams.Find(ctx, examID, LockNone)
		return err
	})
	return e, err
}

// authorize loads the exam, unlocked, to learn its course and runs check on that course.
func (s *ExamService) authorize(ctx context.Context, p auth.Principal, examID id.ID, check func(context.Context, auth.Principal, id.ID) error) error {
	e, err := s.find(ctx, examID)
	if err != nil {
		return err
	}
	return check(ctx, p, e.CourseID)
}

// edit authorizes p to manage the exam's course, then, holding the exam's row lock, builds the
// replacement with change and refuses it when it would break existing attempts.
func (s *ExamService) edit(ctx context.Context, p auth.Principal, examID id.ID, change func(cur domain.Exam, now time.Time) (domain.Exam, error)) (ExamDetail, error) {
	if err := s.authorize(ctx, p, examID, s.courses.CanManageCourse); err != nil {
		return ExamDetail{}, err
	}
	var out ExamDetail
	err := s.tx.RunInTx(ctx, func(r Repos) error {
		cur, err := r.Exams.Find(ctx, examID, LockUpdate)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if err := settleExamAttempts(ctx, r, cur, now); err != nil {
			return err
		}
		next, err := change(cur, now)
		if err != nil {
			return err
		}
		locks, err := r.Exams.Locks(ctx, examID)
		if err != nil {
			return err
		}
		if err := domain.CheckExamEdit(cur, next, locks); err != nil {
			return err
		}
		if err := r.Exams.Replace(ctx, next); err != nil {
			return err
		}
		out = ExamDetail{Exam: next, Locks: locks}
		return nil
	})
	return out, err
}

func (s *ExamService) buildExam(base domain.Exam, in ExamInput, owned map[id.ID]struct{}) (domain.Exam, error) {
	questions, err := buildQuestions(s.ids, in.Questions, owned)
	if err != nil {
		return domain.Exam{}, err
	}
	if err := in.apply(&base); err != nil {
		return domain.Exam{}, err
	}
	base.Position, base.Questions = in.Position, questions
	return validExam(base)
}

// apply copies the settings onto e.
func (st ExamSettings) apply(e *domain.Exam) error {
	const maxSeconds = int(domain.MaxTimeLimit / time.Second)
	var limit time.Duration
	if st.TimeLimitSeconds != nil {
		secs := *st.TimeLimitSeconds
		if secs < 1 || secs > maxSeconds {
			return fmt.Errorf("%w: time_limit_seconds must be 1 to %d", ErrInvalidInput, maxSeconds)
		}
		limit = time.Duration(secs) * time.Second
	}
	e.Title, e.Description, e.PassMark, e.TimeLimit = st.Title, st.Description, st.PassMark, limit
	e.RetakesAllowed, e.OpensAt, e.ClosesAt = st.RetakesAllowed, st.OpensAt, st.ClosesAt
	e.RevealPolicy = domain.RevealPolicy(st.RevealPolicy)
	return nil
}

func validExam(e domain.Exam) (domain.Exam, error) {
	out, err := domain.NewExam(e)
	if err != nil {
		return domain.Exam{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return out, nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
