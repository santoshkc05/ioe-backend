package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

var refundedIn = app.PurchaseRefundedInput{
	EventID: "ev-7", UserID: "200", CourseID: "11", CourseTitle: "Go", AmountMinor: 150000, Currency: "NPR",
	Gateway: "esewa", Reference: "RF-1", AccessRevoked: true, RefundedAt: time.Date(2026, 10, 8, 4, 15, 0, 0, time.UTC),
}

func TestSendPurchaseRefunded(t *testing.T) {
	m, r, u := &fakeMailer{}, &fakeRenderer{}, &fakeUsers{email: "s@example.com", name: " Sita "}
	if err := app.NewService(m, r, u, "").SendPurchaseRefunded(context.Background(), refundedIn); err != nil {
		t.Fatal(err)
	}
	want := app.PurchaseRefundedEmail{Name: "Sita", CourseTitle: "Go", Amount: "NPR 1,500.00", Method: "eSewa",
		Reference: "RF-1", RefundedOn: "8 October 2026, 10:00 NPT", AccessRevoked: true}
	if u.asked != "200" || r.refunded != want {
		t.Fatalf("asked=%q rendered=%+v", u.asked, r.refunded)
	}
	if m.calls != 1 || m.email.To != "s@example.com" || m.email.Subject != "Refunded" ||
		m.key != "ioe:payment.purchase.refunded:ev-7:refunded-v1" {
		t.Fatalf("calls=%d email=%+v key=%q", m.calls, m.email, m.key)
	}
}

func TestSendPurchaseRefundedPermanentFailures(t *testing.T) {
	cases := map[string]struct {
		in    app.PurchaseRefundedInput
		users *fakeUsers
	}{
		"no event id":  {app.PurchaseRefundedInput{UserID: "200"}, &fakeUsers{email: "s@example.com"}},
		"no user id":   {app.PurchaseRefundedInput{EventID: "ev"}, &fakeUsers{email: "s@example.com"}},
		"unknown user": {refundedIn, &fakeUsers{err: app.ErrUnknownUser}},
		"no email":     {refundedIn, &fakeUsers{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := &fakeMailer{}
			err := app.NewService(m, &fakeRenderer{}, c.users, "").SendPurchaseRefunded(context.Background(), c.in)
			if !errors.Is(err, app.ErrPermanent) || m.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, m.calls)
			}
		})
	}
}

func TestSendPurchaseRefundedRetriesContactFailure(t *testing.T) {
	down := errors.New("down")
	err := app.NewService(&fakeMailer{}, &fakeRenderer{}, &fakeUsers{err: down}, "").SendPurchaseRefunded(context.Background(), refundedIn)
	if !errors.Is(err, down) || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("err = %v", err)
	}
}
