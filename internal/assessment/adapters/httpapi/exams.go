package httpapi

import (
	"context"
	"net/http"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/assessment/domain"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// ExamService is the exam use-case surface the handlers call.
type ExamService interface {
	ListAuthoring(ctx context.Context, p auth.Principal, courseID id.ID, version int) ([]domain.Exam, error)
	GetAuthoring(ctx context.Context, p auth.Principal, examID id.ID) (domain.Exam, error)
	Create(ctx context.Context, p auth.Principal, courseID id.ID, in app.ExamInput) (domain.Exam, error)
	Save(ctx context.Context, p auth.Principal, examID id.ID, in app.ExamInput) (domain.Exam, error)
	SaveSettings(ctx context.Context, p auth.Principal, examID id.ID, in app.ExamSettingsInput) (domain.Exam, error)
	Reorder(ctx context.Context, p auth.Principal, courseID id.ID, examIDs []id.ID) error
	Publish(ctx context.Context, p auth.Principal, examID id.ID) error
	Unpublish(ctx context.Context, p auth.Principal, examID id.ID) error
	Duplicate(ctx context.Context, p auth.Principal, examID id.ID) (domain.Exam, error)
	Delete(ctx context.Context, p auth.Principal, examID id.ID) error
	ListAttempts(ctx context.Context, p auth.Principal, examID id.ID) ([]domain.ExamAttempt, error)
	List(ctx context.Context, p auth.Principal, courseID id.ID) ([]app.StudentExam, error)
	Get(ctx context.Context, p auth.Principal, examID id.ID) (domain.Exam, error)
	Start(ctx context.Context, p auth.Principal, examID id.ID) (app.AttemptDetail, error)
	SaveAnswer(ctx context.Context, p auth.Principal, attemptID id.ID, in app.AnswerInput) error
	Submit(ctx context.Context, p auth.Principal, attemptID id.ID) (app.AttemptDetail, error)
	GetAttempt(ctx context.Context, p auth.Principal, attemptID id.ID) (app.AttemptDetail, error)
	Review(ctx context.Context, p auth.Principal, attemptID id.ID) (app.AttemptDetail, error)
}

func (h *Handler) registerExams(r *httpserver.Router, a func(http.HandlerFunc) http.Handler) {
	r.Handle("GET /v1/courses/{courseID}/exams/authoring", a(h.listExamsAuthoring))
	r.Handle("POST /v1/courses/{courseID}/exams", a(h.createExam))
	r.Handle("PUT /v1/courses/{courseID}/exams-order", a(h.reorderExams))
	r.Handle("GET /v1/courses/{courseID}/exams", a(h.listExams))
	r.Handle("GET /v1/exams/{examID}/authoring", a(h.getExamAuthoring))
	r.Handle("PUT /v1/exams/{examID}", a(h.saveExam))
	r.Handle("PATCH /v1/exams/{examID}/settings", a(h.saveExamSettings))
	r.Handle("POST /v1/exams/{examID}/publish", a(h.publishExam))
	r.Handle("POST /v1/exams/{examID}/unpublish", a(h.unpublishExam))
	r.Handle("POST /v1/exams/{examID}/duplicate", a(h.duplicateExam))
	r.Handle("DELETE /v1/exams/{examID}", a(h.deleteExam))
	r.Handle("GET /v1/exams/{examID}/attempts", a(h.listExamAttempts))
	r.Handle("GET /v1/exams/{examID}", a(h.getExam))
	r.Handle("POST /v1/exams/{examID}/attempts", a(h.startExamAttempt))
	r.Handle("POST /v1/exam-attempts/{attemptID}/answers", a(h.saveExamAnswer))
	r.Handle("POST /v1/exam-attempts/{attemptID}/submit", a(h.submitExam))
	r.Handle("GET /v1/exam-attempts/{attemptID}", a(h.getExamAttempt))
	r.Handle("GET /v1/exam-attempts/{attemptID}/review", a(h.getExamReview))
}

// respond writes v as JSON with status, an empty body when v is nil, or err.
func (h *Handler) respond(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	switch {
	case err != nil:
		h.writeError(w, r, err)
	case v == nil:
		w.WriteHeader(status)
	default:
		httpserver.WriteJSON(w, status, v)
	}
}

func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func (h *Handler) listExamsAuthoring(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	ver, err := versionParam(r)
	if err != nil {
		h.respond(w, r, 0, nil, err)
		return
	}
	exams, err := h.exams.ListAuthoring(r.Context(), principal(r), ids[0], ver)
	h.respond(w, r, http.StatusOK, mapSlice(exams, toExamAuthoringSummaryWire), err)
}

func (h *Handler) createExam(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req saveExamRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	d, err := h.exams.Create(r.Context(), principal(r), ids[0], req.toInput())
	h.respond(w, r, http.StatusCreated, toExamAuthoringWire(d), err)
}

func (h *Handler) reorderExams(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	var req reorderExamsRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	h.respond(w, r, http.StatusNoContent, nil, h.exams.Reorder(r.Context(), principal(r), ids[0], req.ExamIDs))
}

func (h *Handler) listExams(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "courseID")
	if !ok {
		return
	}
	exams, err := h.exams.List(r.Context(), principal(r), ids[0])
	h.respond(w, r, http.StatusOK, mapSlice(exams, toExamSummaryWire), err)
}

func (h *Handler) getExamAuthoring(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	d, err := h.exams.GetAuthoring(r.Context(), principal(r), ids[0])
	h.respond(w, r, http.StatusOK, toExamAuthoringWire(d), err)
}

func (h *Handler) saveExam(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	var req saveExamRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	d, err := h.exams.Save(r.Context(), principal(r), ids[0], req.toInput())
	h.respond(w, r, http.StatusOK, toExamAuthoringWire(d), err)
}

func (h *Handler) saveExamSettings(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	var req saveExamSettingsRequest
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	d, err := h.exams.SaveSettings(r.Context(), principal(r), ids[0], req.toInput())
	h.respond(w, r, http.StatusOK, toExamAuthoringWire(d), err)
}

func (h *Handler) publishExam(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	h.respond(w, r, http.StatusNoContent, nil, h.exams.Publish(r.Context(), principal(r), ids[0]))
}

func (h *Handler) unpublishExam(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	h.respond(w, r, http.StatusNoContent, nil, h.exams.Unpublish(r.Context(), principal(r), ids[0]))
}

func (h *Handler) duplicateExam(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	d, err := h.exams.Duplicate(r.Context(), principal(r), ids[0])
	h.respond(w, r, http.StatusCreated, toExamAuthoringWire(d), err)
}

func (h *Handler) deleteExam(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	h.respond(w, r, http.StatusNoContent, nil, h.exams.Delete(r.Context(), principal(r), ids[0]))
}

func (h *Handler) listExamAttempts(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	attempts, err := h.exams.ListAttempts(r.Context(), principal(r), ids[0])
	h.respond(w, r, http.StatusOK, mapSlice(attempts, toExamAttemptSummaryWire), err)
}

func (h *Handler) getExam(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	e, err := h.exams.Get(r.Context(), principal(r), ids[0])
	h.respond(w, r, http.StatusOK, toExamWire(e), err)
}

func (h *Handler) startExamAttempt(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "examID")
	if !ok {
		return
	}
	d, err := h.exams.Start(r.Context(), principal(r), ids[0])
	h.respond(w, r, http.StatusCreated, toExamAttemptWire(d), err)
}

func (h *Handler) saveExamAnswer(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "attemptID")
	if !ok {
		return
	}
	var req answerWire
	if !httpserver.DecodeJSON(w, r, &req) {
		return
	}
	in := app.AnswerInput{QuestionID: req.QuestionID, OptionIDs: req.OptionIDs}
	h.respond(w, r, http.StatusNoContent, nil, h.exams.SaveAnswer(r.Context(), principal(r), ids[0], in))
}

func (h *Handler) submitExam(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "attemptID")
	if !ok {
		return
	}
	d, err := h.exams.Submit(r.Context(), principal(r), ids[0])
	h.respond(w, r, http.StatusOK, toExamAttemptWire(d), err)
}

func (h *Handler) getExamAttempt(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "attemptID")
	if !ok {
		return
	}
	d, err := h.exams.GetAttempt(r.Context(), principal(r), ids[0])
	h.respond(w, r, http.StatusOK, toExamAttemptWire(d), err)
}

func (h *Handler) getExamReview(w http.ResponseWriter, r *http.Request) {
	ids, ok := pathIDs(w, r, "attemptID")
	if !ok {
		return
	}
	d, err := h.exams.Review(r.Context(), principal(r), ids[0])
	if err != nil { // toReviewWire needs a graded attempt
		h.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toReviewWire(d))
}
