package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/adapters/postgres/sqlcgen"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// The jsonb documents below are this adapter's storage format; IDs are JSON strings.

type optionDoc struct {
	ID        id.ID  `json:"id"`
	Label     string `json:"label"`
	IsCorrect bool   `json:"is_correct"`
}

type questionDoc struct {
	ID                 id.ID       `json:"id"`
	Prompt             string      `json:"prompt"`
	Type               string      `json:"type"`
	Explanation        string      `json:"explanation"`
	Points             int         `json:"points"`
	ReferenceLectureID *id.ID      `json:"reference_lecture_id,omitempty"`
	Options            []optionDoc `json:"options"`
}

type answerDoc struct {
	QuestionID id.ID   `json:"question_id"`
	OptionIDs  []id.ID `json:"option_ids"`
}

type quizzes struct{ q *sqlcgen.Queries }

func (r quizzes) Find(ctx context.Context, quizID id.ID) (domain.Quiz, error) {
	row, err := r.q.GetQuizHead(ctx, int64(quizID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Quiz{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Quiz{}, err
	}
	return toQuiz(row)
}

func (r quizzes) FindForUpdate(ctx context.Context, quizID id.ID) (domain.Quiz, error) {
	if _, err := r.q.LockQuiz(ctx, int64(quizID)); errors.Is(err, pgx.ErrNoRows) {
		return domain.Quiz{}, app.ErrNotFound
	} else if err != nil {
		return domain.Quiz{}, err
	}
	return r.Find(ctx, quizID)
}

func (r quizzes) FindRevisions(ctx context.Context, revs map[id.ID]int) ([]domain.Quiz, error) {
	ids, numbers := revisionArgs(revs)
	rows, err := r.q.ListQuizRevisions(ctx, sqlcgen.ListQuizRevisionsParams{QuizIds: ids, Revisions: numbers})
	if err != nil {
		return nil, err
	}
	return toQuizzes(rows)
}

func (r quizzes) ListByLecture(ctx context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error) {
	rows, err := r.q.ListQuizzesByLecture(ctx, sqlcgen.ListQuizzesByLectureParams{CourseID: int64(courseID), LectureID: int64(lectureID)})
	if err != nil {
		return nil, err
	}
	return toQuizzes(rows)
}

func (r quizzes) Insert(ctx context.Context, q domain.Quiz, by id.ID) error {
	if q.Revision != 1 {
		return fmt.Errorf("insert quiz %s: revision %d, want 1", q.ID, q.Revision)
	}
	if err := r.q.InsertQuiz(ctx, sqlcgen.InsertQuizParams{
		ID: int64(q.ID), CourseID: int64(q.CourseID), LectureID: int64(q.LectureID), CreatedAt: q.CreatedAt,
	}); err != nil {
		return err
	}
	return r.insertRevision(ctx, q, by)
}

func (r quizzes) AppendRevision(ctx context.Context, q domain.Quiz, by id.ID) error {
	n, err := r.q.MoveQuizHead(ctx, sqlcgen.MoveQuizHeadParams{ID: int64(q.ID), HeadRevision: int32(q.Revision), UpdatedAt: q.UpdatedAt}) //nolint:gosec // one per edit
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrNotFound
	}
	return r.insertRevision(ctx, q, by)
}

func (r quizzes) insertRevision(ctx context.Context, q domain.Quiz, by id.ID) error {
	doc, err := questionsJSON(q.Questions)
	if err != nil {
		return err
	}
	return r.q.InsertQuizRevision(ctx, sqlcgen.InsertQuizRevisionParams{
		QuizID: int64(q.ID), Revision: int32(q.Revision), Position: int32(q.Position), //nolint:gosec // domain.NewQuiz bounds position; revisions grow one per edit
		Questions: doc, CreatedBy: int64(by), CreatedAt: q.UpdatedAt,
	})
}

func (r quizzes) Delete(ctx context.Context, quizID id.ID, now time.Time) error {
	return r.q.SoftDeleteQuiz(ctx, sqlcgen.SoftDeleteQuizParams{ID: int64(quizID), DeletedAt: &now})
}

func (r quizzes) Undelete(ctx context.Context, quizID id.ID, now time.Time) error {
	return r.q.UndeleteQuiz(ctx, sqlcgen.UndeleteQuizParams{ID: int64(quizID), UpdatedAt: now})
}

func (r quizzes) Heads(ctx context.Context, courseID id.ID) ([]app.Head, error) {
	rows, err := r.q.ListQuizHeads(ctx, int64(courseID))
	if err != nil {
		return nil, err
	}
	out := make([]app.Head, len(rows))
	for i, row := range rows {
		out[i] = app.Head{Ref: app.Ref{Kind: app.KindQuiz, ID: id.ID(row.ID)}, Revision: int(row.HeadRevision), Deleted: row.Deleted}
	}
	return out, nil
}

func (r quizzes) LecturesOf(ctx context.Context, courseID id.ID, quizIDs []id.ID) (map[id.ID]id.ID, error) {
	raw := make([]int64, len(quizIDs))
	for i, v := range quizIDs {
		raw[i] = int64(v)
	}
	rows, err := r.q.ListQuizLectures(ctx, sqlcgen.ListQuizLecturesParams{CourseID: int64(courseID), QuizIds: raw})
	if err != nil {
		return nil, err
	}
	out := make(map[id.ID]id.ID, len(rows))
	for _, row := range rows {
		out[id.ID(row.ID)] = id.ID(row.LectureID)
	}
	return out, nil
}

func (r quizzes) RecordAttempt(ctx context.Context, a domain.QuizAttempt) (id.ID, error) {
	docs := make([]answerDoc, len(a.Answers))
	for i, ans := range a.Answers {
		opts := ans.OptionIDs
		if opts == nil {
			opts = []id.ID{}
		}
		docs[i] = answerDoc{QuestionID: ans.QuestionID, OptionIDs: opts}
	}
	answers, err := json.Marshal(docs)
	if err != nil {
		return 0, err
	}
	var key *string
	if a.IdempotencyKey != "" {
		key = &a.IdempotencyKey
	}
	got, err := r.q.InsertQuizAttempt(ctx, sqlcgen.InsertQuizAttemptParams{
		ID: int64(a.ID), QuizID: int64(a.QuizID), Revision: int32(a.Revision), //nolint:gosec // bounded by stored revisions
		UserID: int64(a.UserID), Answers: answers,
		IdempotencyKey: key, SubmittedAt: a.SubmittedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The key was used before; ON CONFLICT waited for that row to commit, so it is visible.
		got, err = r.q.FindQuizAttemptByKey(ctx, sqlcgen.FindQuizAttemptByKeyParams{
			QuizID: int64(a.QuizID), UserID: int64(a.UserID), IdempotencyKey: key,
		})
	}
	if err != nil {
		return 0, err
	}
	return id.ID(got), nil
}

func questionsJSON(qs []domain.Question) (json.RawMessage, error) {
	docs := make([]questionDoc, len(qs))
	for i, q := range qs {
		opts := make([]optionDoc, len(q.Options))
		for j, o := range q.Options {
			opts[j] = optionDoc{ID: o.ID, Label: o.Label, IsCorrect: o.IsCorrect}
		}
		docs[i] = questionDoc{ID: q.ID, Prompt: q.Prompt, Type: string(q.Type), Explanation: q.Explanation, Points: q.Points, Options: opts}
		if !q.ReferenceLectureID.IsZero() {
			ref := q.ReferenceLectureID
			docs[i].ReferenceLectureID = &ref
		}
	}
	return json.Marshal(docs)
}

// toQuiz rebuilds the quiz without re-validating: stored rows were validated on write.
func toQuiz(row sqlcgen.AssessmentQuizRevisionRow) (domain.Quiz, error) {
	qs, err := toQuestions(row.Questions)
	if err != nil {
		return domain.Quiz{}, err
	}
	return domain.Quiz{ID: id.ID(row.ID), CourseID: id.ID(row.CourseID), LectureID: id.ID(row.LectureID),
		Revision: int(row.Revision), Position: int(row.Position), Questions: qs,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, nil
}

func toQuizzes(rows []sqlcgen.AssessmentQuizRevisionRow) ([]domain.Quiz, error) {
	out := make([]domain.Quiz, len(rows))
	for i, row := range rows {
		var err error
		if out[i], err = toQuiz(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// revisionArgs flattens revs into the parallel arrays the revision queries unnest.
func revisionArgs(revs map[id.ID]int) ([]int64, []int32) {
	ids, numbers := make([]int64, 0, len(revs)), make([]int32, 0, len(revs))
	for k, v := range revs {
		ids, numbers = append(ids, int64(k)), append(numbers, int32(v)) //nolint:gosec // revisions grow one per edit
	}
	return ids, numbers
}

func toQuestions(raw json.RawMessage) ([]domain.Question, error) {
	var docs []questionDoc
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, err
	}
	qs := make([]domain.Question, len(docs))
	for i, d := range docs {
		opts := make([]domain.Option, len(d.Options))
		for j, o := range d.Options {
			opts[j] = domain.Option{ID: o.ID, Label: o.Label, IsCorrect: o.IsCorrect}
		}
		qs[i] = domain.Question{ID: d.ID, Prompt: d.Prompt, Type: domain.QuestionType(d.Type), Explanation: d.Explanation, Points: d.Points, Options: opts}
		if d.ReferenceLectureID != nil {
			qs[i].ReferenceLectureID = *d.ReferenceLectureID
		}
	}
	return qs, nil
}
