package postgres

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
	oneOpenAttemptIndex = "exam_attempts_one_open_idx"
)

// examAnswerDoc is one value of an attempt's answers object, keyed by question ID. The grading
// fields are absent until the attempt is graded.
type examAnswerDoc struct {
	OptionIDs      []id.ID `json:"option_ids"`
	IsCorrect      *bool   `json:"is_correct,omitempty"`
	PointsPossible int     `json:"points_possible,omitempty"`
	PointsAwarded  *int    `json:"points_awarded,omitempty"`
}

type exams struct{ q *sqlcgen.Queries }

func (r exams) Find(ctx context.Context, examID id.ID, lock app.LockMode) (domain.Exam, error) {
	var row sqlcgen.AssessmentExam
	var err error
	switch lock {
	case app.LockShare:
		row, err = r.q.GetExamForShare(ctx, int64(examID))
	case app.LockUpdate:
		row, err = r.q.GetExamForUpdate(ctx, int64(examID))
	default:
		row, err = r.q.GetExam(ctx, int64(examID))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Exam{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Exam{}, err
	}
	return toExam(row)
}

func (r exams) ListByCourse(ctx context.Context, courseID id.ID, publishedOnly bool) ([]domain.Exam, error) {
	rows, err := r.q.ListExamsByCourse(ctx, sqlcgen.ListExamsByCourseParams{CourseID: int64(courseID), PublishedOnly: publishedOnly})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Exam, len(rows))
	for i, row := range rows {
		if out[i], err = toExam(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r exams) Insert(ctx context.Context, e domain.Exam) error {
	doc, err := questionsJSON(e.Questions)
	if err != nil {
		return err
	}
	return r.q.InsertExam(ctx, sqlcgen.InsertExamParams{
		ID: int64(e.ID), CourseID: int64(e.CourseID), Title: e.Title, Description: e.Description, Position: i32(e.Position),
		Status: string(e.Status), PassMark: i32(e.PassMark), TimeLimitSeconds: limitSeconds(e.TimeLimit),
		RetakesAllowed: e.RetakesAllowed, OpensAt: e.OpensAt, ClosesAt: e.ClosesAt, RevealPolicy: string(e.RevealPolicy),
		Questions: doc, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	})
}

func (r exams) Replace(ctx context.Context, e domain.Exam) error {
	doc, err := questionsJSON(e.Questions)
	if err != nil {
		return err
	}
	n, err := r.q.ReplaceExam(ctx, sqlcgen.ReplaceExamParams{
		ID: int64(e.ID), Title: e.Title, Description: e.Description, Position: i32(e.Position), Status: string(e.Status),
		PassMark: i32(e.PassMark), TimeLimitSeconds: limitSeconds(e.TimeLimit), RetakesAllowed: e.RetakesAllowed,
		OpensAt: e.OpensAt, ClosesAt: e.ClosesAt, RevealPolicy: string(e.RevealPolicy), Questions: doc, UpdatedAt: e.UpdatedAt,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrNotFound
	}
	return nil
}

func (r exams) Delete(ctx context.Context, examID id.ID) error {
	err := r.q.DeleteExam(ctx, int64(examID))
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
		return app.ErrExamHasAttempts
	}
	return err
}

func (r exams) NextPosition(ctx context.Context, courseID id.ID) (int, error) {
	next, err := r.q.NextExamPosition(ctx, int64(courseID))
	return int(next), err
}

func (r exams) SetPositions(ctx context.Context, courseID id.ID, examIDs []id.ID) error {
	raw := make([]int64, len(examIDs))
	for i, v := range examIDs {
		raw[i] = int64(v)
	}
	return r.q.SetExamPositions(ctx, sqlcgen.SetExamPositionsParams{CourseID: int64(courseID), ExamIds: raw})
}

func (r exams) Locks(ctx context.Context, examID id.ID) (domain.Locks, error) {
	counts, err := r.q.CountExamAttempts(ctx, int64(examID))
	if err != nil {
		return domain.Locks{}, err
	}
	answered, err := r.q.ListAnsweredExamQuestions(ctx, int64(examID))
	if err != nil {
		return domain.Locks{}, err
	}
	l := domain.Locks{OpenAttempts: int(counts.OpenAttempts), SubmittedAttempts: int(counts.SubmittedAttempts),
		AnsweredQuestionIDs: make(map[id.ID]struct{}, len(answered))}
	for _, raw := range answered {
		v, err := id.Parse(raw)
		if err != nil {
			return domain.Locks{}, err
		}
		l.AnsweredQuestionIDs[v] = struct{}{}
	}
	return l, nil
}

func (r exams) FindAttempt(ctx context.Context, attemptID id.ID, forUpdate bool) (domain.ExamAttempt, error) {
	get := r.q.GetExamAttempt
	if forUpdate {
		get = r.q.GetExamAttemptForUpdate
	}
	return toAttemptOrNotFound(get(ctx, int64(attemptID)))
}

func (r exams) FindOpenAttempt(ctx context.Context, examID, userID id.ID) (domain.ExamAttempt, error) {
	return toAttemptOrNotFound(r.q.GetOpenExamAttempt(ctx, sqlcgen.GetOpenExamAttemptParams{ExamID: int64(examID), UserID: int64(userID)}))
}

func (r exams) HasSubmitted(ctx context.Context, examID, userID id.ID) (bool, error) {
	return r.q.HasSubmittedExamAttempt(ctx, sqlcgen.HasSubmittedExamAttemptParams{ExamID: int64(examID), UserID: int64(userID)})
}

func (r exams) InsertAttempt(ctx context.Context, a domain.ExamAttempt) error {
	err := r.q.InsertExamAttempt(ctx, sqlcgen.InsertExamAttemptParams{
		ID: int64(a.ID), ExamID: int64(a.ExamID), CourseID: int64(a.CourseID), UserID: int64(a.UserID), StartedAt: a.StartedAt,
	})
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == oneOpenAttemptIndex {
		return app.ErrOpenAttemptExists
	}
	return err
}

func (r exams) MergeAnswer(ctx context.Context, attemptID id.ID, a domain.ExamAnswer) error {
	patch, err := json.Marshal(map[string]examAnswerDoc{a.QuestionID.String(): {OptionIDs: nonNil(a.OptionIDs)}})
	if err != nil {
		return err
	}
	n, err := r.q.MergeExamAnswer(ctx, sqlcgen.MergeExamAnswerParams{Patch: patch, ID: int64(attemptID)})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrAttemptSubmitted
	}
	return nil
}

func (r exams) SaveResult(ctx context.Context, a domain.ExamAttempt) error {
	docs := make(map[string]examAnswerDoc, len(a.Answers))
	for _, ans := range a.Answers {
		docs[ans.QuestionID.String()] = examAnswerDoc{OptionIDs: nonNil(ans.OptionIDs), IsCorrect: ans.IsCorrect,
			PointsPossible: ans.PointsPossible, PointsAwarded: ans.PointsAwarded}
	}
	answers, err := json.Marshal(docs)
	if err != nil {
		return err
	}
	var score *int32
	if a.Score != nil {
		v := i32(*a.Score)
		score = &v
	}
	n, err := r.q.SaveExamResult(ctx, sqlcgen.SaveExamResultParams{ID: int64(a.ID), SubmittedAt: a.SubmittedAt, Score: score,
		Passed: a.Passed, AutoSubmitted: a.AutoSubmitted, Answers: answers})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrAttemptSubmitted
	}
	return nil
}

func (r exams) ListAttempts(ctx context.Context, examID id.ID) ([]domain.ExamAttempt, error) {
	return toAttempts(r.q.ListExamAttempts(ctx, int64(examID)))
}

func (r exams) ListUserAttempts(ctx context.Context, courseID, userID id.ID) ([]domain.ExamAttempt, error) {
	return toAttempts(r.q.ListUserExamAttempts(ctx, sqlcgen.ListUserExamAttemptsParams{CourseID: int64(courseID), UserID: int64(userID)}))
}

// toExam rebuilds the exam without re-validating: stored rows were validated on write.
func toExam(row sqlcgen.AssessmentExam) (domain.Exam, error) {
	qs, err := toQuestions(row.Questions)
	if err != nil {
		return domain.Exam{}, err
	}
	e := domain.Exam{ID: id.ID(row.ID), CourseID: id.ID(row.CourseID), Title: row.Title, Description: row.Description,
		Position: int(row.Position), Status: domain.ExamStatus(row.Status), PassMark: int(row.PassMark),
		RetakesAllowed: row.RetakesAllowed, OpensAt: utc(row.OpensAt), ClosesAt: utc(row.ClosesAt),
		RevealPolicy: domain.RevealPolicy(row.RevealPolicy), Questions: qs, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
	if row.TimeLimitSeconds != nil {
		e.TimeLimit = time.Duration(*row.TimeLimitSeconds) * time.Second
	}
	return e, nil
}

func toAttemptOrNotFound(row sqlcgen.AssessmentExamAttempt, err error) (domain.ExamAttempt, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ExamAttempt{}, app.ErrNotFound
	}
	if err != nil {
		return domain.ExamAttempt{}, err
	}
	return toAttempt(row)
}

func toAttempts(rows []sqlcgen.AssessmentExamAttempt, err error) ([]domain.ExamAttempt, error) {
	if err != nil {
		return nil, err
	}
	out := make([]domain.ExamAttempt, len(rows))
	for i, row := range rows {
		if out[i], err = toAttempt(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// toAttempt returns the answers sorted by question ID; callers order them by the exam.
func toAttempt(row sqlcgen.AssessmentExamAttempt) (domain.ExamAttempt, error) {
	var docs map[string]examAnswerDoc
	if err := json.Unmarshal(row.Answers, &docs); err != nil {
		return domain.ExamAttempt{}, err
	}
	answers := make([]domain.ExamAnswer, 0, len(docs))
	for raw, d := range docs {
		qid, err := id.Parse(raw)
		if err != nil {
			return domain.ExamAttempt{}, err
		}
		answers = append(answers, domain.ExamAnswer{QuestionID: qid, OptionIDs: nonNil(d.OptionIDs), IsCorrect: d.IsCorrect,
			PointsPossible: d.PointsPossible, PointsAwarded: d.PointsAwarded})
	}
	slices.SortFunc(answers, func(a, b domain.ExamAnswer) int { return cmp.Compare(a.QuestionID, b.QuestionID) })
	a := domain.ExamAttempt{ID: id.ID(row.ID), ExamID: id.ID(row.ExamID), CourseID: id.ID(row.CourseID), UserID: id.ID(row.UserID),
		StartedAt: row.StartedAt.UTC(), SubmittedAt: utc(row.SubmittedAt), Passed: row.Passed, AutoSubmitted: row.AutoSubmitted,
		Answers: answers}
	if row.Score != nil {
		score := int(*row.Score)
		a.Score = &score
	}
	return a, nil
}

// i32 narrows a value domain.NewExam bounds well inside int32 (position, pass mark, seconds, score).
func i32(v int) int32 {
	return int32(v) //nolint:gosec // bounded by domain validation
}

func limitSeconds(d time.Duration) *int32 {
	if d == 0 {
		return nil
	}
	v := i32(int(d / time.Second))
	return &v
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func nonNil(ids []id.ID) []id.ID {
	if ids == nil {
		return []id.ID{}
	}
	return ids
}
