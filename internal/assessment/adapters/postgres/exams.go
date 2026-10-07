package postgres

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

const (
	uniqueViolation     = "23505"
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
	if lock == app.LockUpdate {
		if _, err := r.q.LockExam(ctx, int64(examID)); errors.Is(err, pgx.ErrNoRows) {
			return domain.Exam{}, app.ErrNotFound
		} else if err != nil {
			return domain.Exam{}, err
		}
	}
	row, err := r.q.GetExamHead(ctx, int64(examID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Exam{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Exam{}, err
	}
	return toExam(row)
}

func (r exams) FindRevisions(ctx context.Context, revs map[id.ID]int) ([]domain.Exam, error) {
	ids, numbers := revisionArgs(revs)
	rows, err := r.q.ListExamRevisions(ctx, sqlcgen.ListExamRevisionsParams{ExamIds: ids, Revisions: numbers})
	if err != nil {
		return nil, err
	}
	return toExams(rows)
}

func (r exams) ListByCourse(ctx context.Context, courseID id.ID) ([]domain.Exam, error) {
	rows, err := r.q.ListExamsByCourse(ctx, int64(courseID))
	if err != nil {
		return nil, err
	}
	return toExams(rows)
}

func (r exams) Insert(ctx context.Context, e domain.Exam, by id.ID) error {
	if e.Revision != 1 {
		return fmt.Errorf("insert exam %s: revision %d, want 1", e.ID, e.Revision)
	}
	if err := r.q.InsertExam(ctx, sqlcgen.InsertExamParams{
		ID: int64(e.ID), CourseID: int64(e.CourseID), CreatedAt: e.CreatedAt,
	}); err != nil {
		return err
	}
	return r.insertRevision(ctx, e, by)
}

func (r exams) AppendRevision(ctx context.Context, e domain.Exam, by id.ID) error {
	n, err := r.q.MoveExamHead(ctx, sqlcgen.MoveExamHeadParams{ID: int64(e.ID), HeadRevision: int32(e.Revision), UpdatedAt: e.UpdatedAt}) //nolint:gosec // one per edit
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrNotFound
	}
	return r.insertRevision(ctx, e, by)
}

func (r exams) insertRevision(ctx context.Context, e domain.Exam, by id.ID) error {
	doc, err := questionsJSON(e.Questions)
	if err != nil {
		return err
	}
	return r.q.InsertExamRevision(ctx, sqlcgen.InsertExamRevisionParams{
		ExamID: int64(e.ID), Revision: int32(e.Revision), Title: e.Title, Description: e.Description, Position: i32(e.Position), //nolint:gosec // revisions grow one per edit
		Status: string(e.Status), PassMark: i32(e.PassMark), TimeLimitSeconds: limitSeconds(e.TimeLimit),
		RetakesAllowed: e.RetakesAllowed, OpensAt: e.OpensAt, ClosesAt: e.ClosesAt, RevealPolicy: string(e.RevealPolicy),
		Questions: doc, CreatedBy: int64(by), CreatedAt: e.UpdatedAt,
	})
}

func (r exams) Delete(ctx context.Context, examID id.ID, now time.Time) error {
	return r.q.SoftDeleteExam(ctx, sqlcgen.SoftDeleteExamParams{ID: int64(examID), DeletedAt: &now})
}

func (r exams) Undelete(ctx context.Context, examID id.ID, now time.Time) error {
	return r.q.UndeleteExam(ctx, sqlcgen.UndeleteExamParams{ID: int64(examID), UpdatedAt: now})
}

func (r exams) Heads(ctx context.Context, courseID id.ID) ([]app.Head, error) {
	rows, err := r.q.ListExamHeads(ctx, int64(courseID))
	if err != nil {
		return nil, err
	}
	out := make([]app.Head, len(rows))
	for i, row := range rows {
		out[i] = app.Head{Ref: app.Ref{Kind: app.KindExam, ID: id.ID(row.ID)}, Revision: int(row.HeadRevision), Deleted: row.Deleted}
	}
	return out, nil
}

func (r exams) NextPosition(ctx context.Context, courseID id.ID) (int, error) {
	next, err := r.q.NextExamPosition(ctx, int64(courseID))
	return int(next), err
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
		ID: int64(a.ID), ExamID: int64(a.ExamID), Revision: int32(a.Revision), CourseID: int64(a.CourseID), UserID: int64(a.UserID), StartedAt: a.StartedAt, //nolint:gosec // bounded by stored revisions
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
func toExam(row sqlcgen.AssessmentExamRevisionRow) (domain.Exam, error) {
	qs, err := toQuestions(row.Questions)
	if err != nil {
		return domain.Exam{}, err
	}
	e := domain.Exam{ID: id.ID(row.ID), CourseID: id.ID(row.CourseID), Revision: int(row.Revision),
		Title: row.Title, Description: row.Description,
		Position: int(row.Position), Status: domain.ExamStatus(row.Status), PassMark: int(row.PassMark),
		RetakesAllowed: row.RetakesAllowed, OpensAt: utc(row.OpensAt), ClosesAt: utc(row.ClosesAt),
		RevealPolicy: domain.RevealPolicy(row.RevealPolicy), Questions: qs, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
	if row.TimeLimitSeconds.Valid {
		e.TimeLimit = time.Duration(row.TimeLimitSeconds.Int32) * time.Second
	}
	return e, nil
}

func toExams(rows []sqlcgen.AssessmentExamRevisionRow) ([]domain.Exam, error) {
	out := make([]domain.Exam, len(rows))
	for i, row := range rows {
		var err error
		if out[i], err = toExam(row); err != nil {
			return nil, err
		}
	}
	return out, nil
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
	a := domain.ExamAttempt{ID: id.ID(row.ID), ExamID: id.ID(row.ExamID), Revision: int(row.Revision), CourseID: id.ID(row.CourseID), UserID: id.ID(row.UserID),
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

func limitSeconds(d time.Duration) pgtype.Int4 {
	if d == 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: i32(int(d / time.Second)), Valid: true}
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
