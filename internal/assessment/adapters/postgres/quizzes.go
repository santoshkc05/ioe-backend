package postgres

import (
	"context"
	"encoding/json"
	"errors"

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
	row, err := r.q.GetQuiz(ctx, int64(quizID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Quiz{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Quiz{}, err
	}
	return toQuiz(row)
}

func (r quizzes) ListByLecture(ctx context.Context, courseID, lectureID id.ID) ([]domain.Quiz, error) {
	rows, err := r.q.ListQuizzesByLecture(ctx, sqlcgen.ListQuizzesByLectureParams{CourseID: int64(courseID), LectureID: int64(lectureID)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Quiz, len(rows))
	for i, row := range rows {
		if out[i], err = toQuiz(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r quizzes) Insert(ctx context.Context, q domain.Quiz) error {
	doc, err := questionsJSON(q.Questions)
	if err != nil {
		return err
	}
	return r.q.InsertQuiz(ctx, sqlcgen.InsertQuizParams{
		ID: int64(q.ID), CourseID: int64(q.CourseID), LectureID: int64(q.LectureID), Position: int32(q.Position), //nolint:gosec // domain bounds position by request size
		Questions: doc, CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt,
	})
}

func (r quizzes) Replace(ctx context.Context, q domain.Quiz) error {
	doc, err := questionsJSON(q.Questions)
	if err != nil {
		return err
	}
	n, err := r.q.ReplaceQuiz(ctx, sqlcgen.ReplaceQuizParams{
		ID: int64(q.ID), Position: int32(q.Position), Questions: doc, UpdatedAt: q.UpdatedAt, //nolint:gosec // see Insert
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrNotFound
	}
	return nil
}

func (r quizzes) Delete(ctx context.Context, quizID id.ID) error {
	return r.q.DeleteQuiz(ctx, int64(quizID))
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
		ID: int64(a.ID), QuizID: int64(a.QuizID), UserID: int64(a.UserID), Answers: answers,
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
func toQuiz(row sqlcgen.AssessmentQuiz) (domain.Quiz, error) {
	var docs []questionDoc
	if err := json.Unmarshal(row.Questions, &docs); err != nil {
		return domain.Quiz{}, err
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
	return domain.Quiz{ID: id.ID(row.ID), CourseID: id.ID(row.CourseID), LectureID: id.ID(row.LectureID), Position: int(row.Position),
		Questions: qs, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, nil
}
