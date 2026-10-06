package httpapi

import (
	"maps"
	"slices"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

type examSettingsRequest struct {
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	PassMark         int        `json:"pass_mark"`
	TimeLimitSeconds *int       `json:"time_limit_seconds"`
	RetakesAllowed   bool       `json:"retakes_allowed"`
	OpensAt          *time.Time `json:"opens_at"`
	ClosesAt         *time.Time `json:"closes_at"`
	RevealPolicy     string     `json:"reveal_policy"`
}

func (r examSettingsRequest) toSettings() app.ExamSettings {
	return app.ExamSettings{Title: r.Title, Description: r.Description, PassMark: r.PassMark, TimeLimitSeconds: r.TimeLimitSeconds,
		RetakesAllowed: r.RetakesAllowed, OpensAt: r.OpensAt, ClosesAt: r.ClosesAt, RevealPolicy: r.RevealPolicy}
}

type saveExamRequest struct {
	examSettingsRequest
	Position  int                 `json:"position"`
	Questions []questionInputWire `json:"questions"`
}

func (r saveExamRequest) toInput() app.ExamInput {
	return app.ExamInput{ExamSettings: r.toSettings(), Position: r.Position, Questions: toQuestionInputs(r.Questions)}
}

type saveExamSettingsRequest struct {
	examSettingsRequest
	Points map[string]int `json:"points"`
}

func (r saveExamSettingsRequest) toInput() app.ExamSettingsInput {
	return app.ExamSettingsInput{ExamSettings: r.toSettings(), Points: r.Points}
}

type reorderExamsRequest struct {
	ExamIDs []id.ID `json:"exam_ids"`
}

type examSettingsWire struct {
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	PassMark         int        `json:"pass_mark"`
	TimeLimitSeconds *int       `json:"time_limit_seconds"` // null when untimed
	RetakesAllowed   bool       `json:"retakes_allowed"`
	OpensAt          *time.Time `json:"opens_at,omitempty"`
	ClosesAt         *time.Time `json:"closes_at,omitempty"`
	RevealPolicy     string     `json:"reveal_policy"`
}

// examQuestionWire is the student view of a question: no answer key, no explanation.
type examQuestionWire struct {
	ID                 id.ID        `json:"id"`
	Prompt             string       `json:"prompt"`
	Type               string       `json:"type"`
	Points             int          `json:"points"`
	Options            []optionWire `json:"options"`
	ReferenceLectureID *id.ID       `json:"reference_lecture_id,omitempty"`
}

type examAuthoringQuestionWire struct {
	examQuestionWire
	CorrectOptionIDs []id.ID `json:"correct_option_ids"`
	Explanation      string  `json:"explanation"`
}

type examWire struct {
	ID       id.ID `json:"id"`
	CourseID id.ID `json:"course_id"`
	examSettingsWire
	Position  int                `json:"position"`
	Questions []examQuestionWire `json:"questions"`
}

type locksWire struct {
	HasOpenAttempts       bool    `json:"has_open_attempts"`
	HasSubmittedAttempts  bool    `json:"has_submitted_attempts"`
	AnsweredQuestionIDs   []id.ID `json:"answered_question_ids"`
	OpenAttemptCount      int     `json:"open_attempt_count"`
	SubmittedAttemptCount int     `json:"submitted_attempt_count"`
}

type examAuthoringWire struct {
	ID       id.ID `json:"id"`
	CourseID id.ID `json:"course_id"`
	examSettingsWire
	Position  int                         `json:"position"`
	Status    string                      `json:"status"`
	Locks     locksWire                   `json:"locks"`
	Questions []examAuthoringQuestionWire `json:"questions"`
}

type examAuthoringSummaryWire struct {
	ID id.ID `json:"id"`
	examSettingsWire
	Position      int    `json:"position"`
	Status        string `json:"status"`
	QuestionCount int    `json:"question_count"`
	TotalPoints   int    `json:"total_points"`
}

type examSummaryWire struct {
	ID id.ID `json:"id"`
	examSettingsWire
	Position      int    `json:"position"`
	QuestionCount int    `json:"question_count"`
	TotalPoints   int    `json:"total_points"`
	Availability  string `json:"availability"`
	OpenAttemptID *id.ID `json:"open_attempt_id,omitempty"`
	BestScore     *int   `json:"best_score,omitempty"`
	BestPassed    *bool  `json:"best_passed,omitempty"`
	AttemptCount  int    `json:"attempt_count"`
}

type examAnswerWire struct {
	QuestionID         id.ID   `json:"question_id"`
	OptionIDs          []id.ID `json:"option_ids"`
	IsCorrect          *bool   `json:"is_correct,omitempty"`
	PointsPossible     *int    `json:"points_possible,omitempty"`
	PointsAwarded      *int    `json:"points_awarded,omitempty"`
	ReferenceLectureID *id.ID  `json:"reference_lecture_id,omitempty"`
}

type examAttemptWire struct {
	ID            id.ID            `json:"id"`
	CourseID      id.ID            `json:"course_id"`
	ExamID        id.ID            `json:"exam_id"`
	UserID        id.ID            `json:"user_id"`
	StartedAt     time.Time        `json:"started_at"`
	Deadline      *time.Time       `json:"deadline,omitempty"`
	SubmittedAt   *time.Time       `json:"submitted_at,omitempty"`
	Score         *int             `json:"score,omitempty"`
	Passed        *bool            `json:"passed,omitempty"`
	AutoSubmitted bool             `json:"auto_submitted"`
	Answers       []examAnswerWire `json:"answers"`
}

type examAttemptSummaryWire struct {
	ID            id.ID      `json:"id"`
	UserID        id.ID      `json:"user_id"`
	StartedAt     time.Time  `json:"started_at"`
	SubmittedAt   *time.Time `json:"submitted_at,omitempty"`
	Score         *int       `json:"score,omitempty"`
	Passed        *bool      `json:"passed,omitempty"`
	AutoSubmitted bool       `json:"auto_submitted"`
	StillOpen     bool       `json:"still_open"`
}

type reviewQuestionWire struct {
	ID                 id.ID        `json:"id"`
	Position           int          `json:"position"`
	Prompt             string       `json:"prompt"`
	Type               string       `json:"type"`
	Options            []optionWire `json:"options"`
	SelectedOptionIDs  []id.ID      `json:"selected_option_ids"`
	CorrectOptionIDs   []id.ID      `json:"correct_option_ids"`
	Explanation        string       `json:"explanation"`
	PointsPossible     int          `json:"points_possible"`
	PointsAwarded      int          `json:"points_awarded"`
	ReferenceLectureID *id.ID       `json:"reference_lecture_id,omitempty"`
}

type reviewWire struct {
	AttemptID     id.ID                `json:"attempt_id"`
	ExamID        id.ID                `json:"exam_id"`
	Title         string               `json:"title"`
	Score         int                  `json:"score"`
	Passed        bool                 `json:"passed"`
	PassMark      int                  `json:"pass_mark"`
	SubmittedAt   time.Time            `json:"submitted_at"`
	AutoSubmitted bool                 `json:"auto_submitted"`
	Questions     []reviewQuestionWire `json:"questions"`
}

func toSettingsWire(e domain.Exam) examSettingsWire {
	w := examSettingsWire{Title: e.Title, Description: e.Description, PassMark: e.PassMark, RetakesAllowed: e.RetakesAllowed,
		OpensAt: e.OpensAt, ClosesAt: e.ClosesAt, RevealPolicy: string(e.RevealPolicy)}
	if e.TimeLimit > 0 {
		secs := int(e.TimeLimit / time.Second)
		w.TimeLimitSeconds = &secs
	}
	return w
}

func refPtr(v id.ID) *id.ID {
	if v.IsZero() {
		return nil
	}
	return &v
}

func toOptionWires(q domain.Question) []optionWire {
	out := make([]optionWire, len(q.Options))
	for i, o := range q.Options {
		out[i] = optionWire{ID: o.ID, Label: o.Label}
	}
	return out
}

func toExamQuestionWire(q domain.Question) examQuestionWire {
	return examQuestionWire{ID: q.ID, Prompt: q.Prompt, Type: string(q.Type), Points: q.Points, Options: toOptionWires(q),
		ReferenceLectureID: refPtr(q.ReferenceLectureID)}
}

func toExamWire(e domain.Exam) examWire {
	return examWire{ID: e.ID, CourseID: e.CourseID, examSettingsWire: toSettingsWire(e), Position: e.Position,
		Questions: mapSlice(e.Questions, toExamQuestionWire)}
}

func toExamAuthoringWire(d app.ExamDetail) examAuthoringWire {
	e, l := d.Exam, d.Locks
	answered := slices.Sorted(maps.Keys(l.AnsweredQuestionIDs))
	if answered == nil {
		answered = []id.ID{}
	}
	return examAuthoringWire{ID: e.ID, CourseID: e.CourseID, examSettingsWire: toSettingsWire(e), Position: e.Position,
		Status: string(e.Status),
		Locks: locksWire{HasOpenAttempts: l.OpenAttempts > 0, HasSubmittedAttempts: l.SubmittedAttempts > 0,
			AnsweredQuestionIDs: answered, OpenAttemptCount: l.OpenAttempts, SubmittedAttemptCount: l.SubmittedAttempts},
		Questions: mapSlice(e.Questions, func(q domain.Question) examAuthoringQuestionWire {
			return examAuthoringQuestionWire{examQuestionWire: toExamQuestionWire(q), CorrectOptionIDs: q.CorrectOptionIDs(),
				Explanation: q.Explanation}
		})}
}

func toExamAuthoringSummaryWire(e domain.Exam) examAuthoringSummaryWire {
	return examAuthoringSummaryWire{ID: e.ID, examSettingsWire: toSettingsWire(e), Position: e.Position, Status: string(e.Status),
		QuestionCount: len(e.Questions), TotalPoints: e.TotalPoints()}
}

func toExamSummaryWire(s app.StudentExam) examSummaryWire {
	e := s.Exam
	return examSummaryWire{ID: e.ID, examSettingsWire: toSettingsWire(e), Position: e.Position, QuestionCount: len(e.Questions),
		TotalPoints: e.TotalPoints(), Availability: string(s.Availability), OpenAttemptID: refPtr(s.OpenAttemptID),
		BestScore: s.BestScore, BestPassed: s.BestPassed, AttemptCount: s.AttemptCount}
}

// answersByQuestion indexes an attempt's answers by question ID.
func answersByQuestion(a domain.ExamAttempt) map[id.ID]domain.ExamAnswer {
	out := make(map[id.ID]domain.ExamAnswer, len(a.Answers))
	for _, ans := range a.Answers {
		out[ans.QuestionID] = ans
	}
	return out
}

func nonNilIDs(ids []id.ID) []id.ID {
	if ids == nil {
		return []id.ID{}
	}
	return ids
}

// toExamAttemptWire lists answers in the exam's question order. Grading fields appear only once
// the attempt is graded.
func toExamAttemptWire(d app.AttemptDetail) examAttemptWire {
	a, e := d.Attempt, d.Exam
	w := examAttemptWire{ID: a.ID, CourseID: a.CourseID, ExamID: a.ExamID, UserID: a.UserID, StartedAt: a.StartedAt,
		Deadline: a.Deadline(e), SubmittedAt: a.SubmittedAt, Score: a.Score, Passed: a.Passed, AutoSubmitted: a.AutoSubmitted,
		Answers: []examAnswerWire{}}
	byQuestion := answersByQuestion(a)
	for _, q := range e.Questions {
		ans, ok := byQuestion[q.ID]
		if !ok {
			continue
		}
		aw := examAnswerWire{QuestionID: q.ID, OptionIDs: nonNilIDs(ans.OptionIDs)}
		if ans.IsCorrect != nil {
			possible := ans.PointsPossible
			aw.IsCorrect, aw.PointsPossible, aw.PointsAwarded = ans.IsCorrect, &possible, ans.PointsAwarded
			aw.ReferenceLectureID = refPtr(q.ReferenceLectureID)
		}
		w.Answers = append(w.Answers, aw)
	}
	return w
}

func toExamAttemptSummaryWire(a domain.ExamAttempt) examAttemptSummaryWire {
	return examAttemptSummaryWire{ID: a.ID, UserID: a.UserID, StartedAt: a.StartedAt, SubmittedAt: a.SubmittedAt, Score: a.Score,
		Passed: a.Passed, AutoSubmitted: a.AutoSubmitted, StillOpen: a.Open()}
}

// toReviewWire pairs each graded answer with the exam's current wording and key. Questions
// added after the attempt was graded have no graded answer and are left out.
func toReviewWire(d app.AttemptDetail) reviewWire {
	a, e := d.Attempt, d.Exam
	w := reviewWire{AttemptID: a.ID, ExamID: e.ID, Title: e.Title, Score: *a.Score, Passed: *a.Passed, PassMark: e.PassMark,
		SubmittedAt: *a.SubmittedAt, AutoSubmitted: a.AutoSubmitted, Questions: []reviewQuestionWire{}}
	byQuestion := answersByQuestion(a)
	for i, q := range e.Questions {
		ans, ok := byQuestion[q.ID]
		if !ok || ans.PointsAwarded == nil {
			continue
		}
		w.Questions = append(w.Questions, reviewQuestionWire{ID: q.ID, Position: i, Prompt: q.Prompt, Type: string(q.Type),
			Options: toOptionWires(q), SelectedOptionIDs: nonNilIDs(ans.OptionIDs), CorrectOptionIDs: q.CorrectOptionIDs(),
			Explanation: q.Explanation, PointsPossible: ans.PointsPossible, PointsAwarded: *ans.PointsAwarded,
			ReferenceLectureID: refPtr(q.ReferenceLectureID)})
	}
	return w
}
