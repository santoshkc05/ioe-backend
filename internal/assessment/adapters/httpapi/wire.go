package httpapi

import (
	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type optionInputWire struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	IsCorrect bool   `json:"is_correct"`
}

type questionInputWire struct {
	ID                 string            `json:"id"`
	Prompt             string            `json:"prompt"`
	Type               string            `json:"type"`
	Explanation        string            `json:"explanation"`
	Points             int               `json:"points"`
	ReferenceLectureID string            `json:"reference_lecture_id"`
	Options            []optionInputWire `json:"options"`
}

type saveQuizRequest struct {
	Position  int                 `json:"position"`
	Questions []questionInputWire `json:"questions"`
}

func (r saveQuizRequest) toInput() app.QuizInput {
	in := app.QuizInput{Position: r.Position, Questions: make([]app.QuestionInput, len(r.Questions))}
	for i, q := range r.Questions {
		opts := make([]app.OptionInput, len(q.Options))
		for j, o := range q.Options {
			opts[j] = app.OptionInput{ID: o.ID, Label: o.Label, IsCorrect: o.IsCorrect}
		}
		in.Questions[i] = app.QuestionInput{ID: q.ID, Prompt: q.Prompt, Type: q.Type, Explanation: q.Explanation,
			Points: q.Points, ReferenceLectureID: q.ReferenceLectureID, Options: opts}
	}
	return in
}

type answerWire struct {
	QuestionID string   `json:"question_id"`
	OptionIDs  []string `json:"option_ids"`
}

type recordAttemptRequest struct {
	UserID  id.ID        `json:"user_id"`
	Answers []answerWire `json:"answers"`
}

func (r recordAttemptRequest) toAnswers() []app.AnswerInput {
	out := make([]app.AnswerInput, len(r.Answers))
	for i, a := range r.Answers {
		out[i] = app.AnswerInput{QuestionID: a.QuestionID, OptionIDs: a.OptionIDs}
	}
	return out
}

type optionWire struct {
	ID    id.ID  `json:"id"`
	Label string `json:"label"`
}

// questionWire carries the answer key: the student client grades locally.
type questionWire struct {
	ID               id.ID        `json:"id"`
	Prompt           string       `json:"prompt"`
	Type             string       `json:"type"`
	Options          []optionWire `json:"options"`
	CorrectOptionIDs []id.ID      `json:"correct_option_ids"`
	Explanation      string       `json:"explanation"`
}

type quizWire struct {
	ID        id.ID          `json:"id"`
	LectureID id.ID          `json:"lecture_id"`
	Position  int            `json:"position"`
	Questions []questionWire `json:"questions"`
}

type attemptWire struct {
	ID       id.ID `json:"id"`
	Recorded bool  `json:"recorded"`
}

func toQuizWire(q domain.Quiz) quizWire {
	w := quizWire{ID: q.ID, LectureID: q.LectureID, Position: q.Position, Questions: make([]questionWire, len(q.Questions))}
	for i, qu := range q.Questions {
		qw := questionWire{ID: qu.ID, Prompt: qu.Prompt, Type: string(qu.Type), Explanation: qu.Explanation,
			Options: make([]optionWire, len(qu.Options)), CorrectOptionIDs: []id.ID{}}
		for j, o := range qu.Options {
			qw.Options[j] = optionWire{ID: o.ID, Label: o.Label}
			if o.IsCorrect {
				qw.CorrectOptionIDs = append(qw.CorrectOptionIDs, o.ID)
			}
		}
		w.Questions[i] = qw
	}
	return w
}
