// Package events subscribes assessment to the domain events it consumes.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/santoshkc2200/ioe-backend/internal/assessment/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
)

// DraftDiscardedTopic is courseauthoring's DraftDiscarded event name.
const DraftDiscardedTopic = "courseauthoring.course.draft_discarded"

const handlerTimeout = 30 * time.Second

type pinDoc struct {
	Kind     string `json:"kind"`
	ID       id.ID  `json:"id"`
	Revision int    `json:"revision"`
}

type draftDiscardedDoc struct {
	CourseID id.ID    `json:"course_id"`
	ActorID  id.ID    `json:"actor_id"`
	Pins     []pinDoc `json:"pins"`
}

// DecodeDraftDiscarded reads a DraftDiscarded payload.
func DecodeDraftDiscarded(msg *message.Message) (courseID, actorID id.ID, pins app.Pins, err error) {
	var doc draftDiscardedDoc
	if err := json.Unmarshal(msg.Payload, &doc); err != nil {
		return 0, 0, nil, fmt.Errorf("decode draft discarded: %w", err)
	}
	pins = make(app.Pins, len(doc.Pins))
	for _, p := range doc.Pins {
		kind := app.Kind(p.Kind)
		if kind != app.KindQuiz && kind != app.KindExam {
			return 0, 0, nil, fmt.Errorf("decode draft discarded: unknown kind %q", p.Kind)
		}
		pins[app.Ref{Kind: kind, ID: p.ID}] = p.Revision
	}
	return doc.CourseID, doc.ActorID, pins, nil
}

// Handlers process the events assessment subscribes to.
type Handlers struct{ restore *app.RestoreService }

func New(restore *app.RestoreService) *Handlers { return &Handlers{restore: restore} }

// DraftDiscarded resets the course's working-copy quizzes and exams to the live pins.
func (h *Handlers) DraftDiscarded(msg *message.Message) error {
	courseID, actorID, pins, err := DecodeDraftDiscarded(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	return h.restore.RestorePins(ctx, courseID, actorID, pins)
}
