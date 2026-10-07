package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

type fakeUsers struct {
	email, name string
	err         error
	asked       string
}

func (f *fakeUsers) Contact(_ context.Context, userID string) (string, string, error) {
	f.asked = userID
	return f.email, f.name, f.err
}

var paidIn = app.PurchasePaidInput{
	EventID: "ev-9", UserID: "200", CourseID: "11", CourseTitle: "Go", AmountMinor: 150000, Currency: "NPR",
	Gateway: "esewa", PaidAt: time.Date(2026, 10, 7, 4, 15, 0, 0, time.UTC),
}

func TestSendPurchasePaid(t *testing.T) {
	m, r, u := &fakeMailer{}, &fakeRenderer{}, &fakeUsers{email: "s@example.com", name: "Sita"}
	if err := app.NewService(m, r, u, "https://app.test/").SendPurchasePaid(context.Background(), paidIn); err != nil {
		t.Fatal(err)
	}
	want := app.PurchasePaidEmail{Name: "Sita", CourseTitle: "Go", Amount: "NPR 1,500.00", Method: "eSewa",
		PaidOn: "7 October 2026, 10:00 NPT", CourseURL: "https://app.test/courses/11"}
	if u.asked != "200" || r.paid != want {
		t.Fatalf("asked=%q rendered=%+v", u.asked, r.paid)
	}
	if m.calls != 1 || m.email.To != "s@example.com" || m.email.Subject != "Paid" || m.key != "ioe:payment.purchase.paid:ev-9:paid-v1" {
		t.Fatalf("calls=%d email=%+v key=%q", m.calls, m.email, m.key)
	}
}

func TestSendPurchasePaidFormatting(t *testing.T) {
	cases := []struct {
		amount int64
		method string
		want   app.PurchasePaidEmail
	}{
		{5, "cash", app.PurchasePaidEmail{Amount: "NPR 0.05", Method: "Cash"}},
		{123456789, "bank_transfer", app.PurchasePaidEmail{Amount: "NPR 1,234,567.89", Method: "Bank transfer"}},
		{100, "other", app.PurchasePaidEmail{Amount: "NPR 1.00", Method: "Other"}},
	}
	for _, c := range cases {
		in := paidIn
		in.AmountMinor, in.Gateway, in.ManualMethod = c.amount, "manual", c.method
		r := &fakeRenderer{}
		if err := app.NewService(&fakeMailer{}, r, &fakeUsers{email: "s@example.com"}, "").SendPurchasePaid(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if r.paid.Amount != c.want.Amount || r.paid.Method != c.want.Method || r.paid.CourseURL != "" {
			t.Fatalf("%d %s: %+v", c.amount, c.method, r.paid)
		}
	}
}

func TestSendPurchasePaidPermanentFailures(t *testing.T) {
	cases := map[string]struct {
		in    app.PurchasePaidInput
		users *fakeUsers
		r     *fakeRenderer
	}{
		"no event id":  {func() app.PurchasePaidInput { in := paidIn; in.EventID = ""; return in }(), &fakeUsers{email: "s@example.com"}, &fakeRenderer{}},
		"no user id":   {func() app.PurchasePaidInput { in := paidIn; in.UserID = ""; return in }(), &fakeUsers{email: "s@example.com"}, &fakeRenderer{}},
		"unknown user": {paidIn, &fakeUsers{err: app.ErrUnknownUser}, &fakeRenderer{}},
		"no email":     {paidIn, &fakeUsers{}, &fakeRenderer{}},
		"render fails": {paidIn, &fakeUsers{email: "s@example.com"}, &fakeRenderer{err: errors.New("bad")}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := &fakeMailer{}
			err := app.NewService(m, c.r, c.users, "").SendPurchasePaid(context.Background(), c.in)
			if !errors.Is(err, app.ErrPermanent) || m.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, m.calls)
			}
		})
	}
}

func TestSendPurchasePaidRetryableFailures(t *testing.T) {
	errDown := errors.New("down")
	err := app.NewService(&fakeMailer{}, &fakeRenderer{}, &fakeUsers{err: errDown}, "").SendPurchasePaid(context.Background(), paidIn)
	if !errors.Is(err, errDown) || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("lookup err = %v", err)
	}
	err = app.NewService(&fakeMailer{err: errDown}, &fakeRenderer{}, &fakeUsers{email: "s@example.com"}, "").SendPurchasePaid(context.Background(), paidIn)
	if !errors.Is(err, errDown) || errors.Is(err, app.ErrPermanent) {
		t.Fatalf("enqueue err = %v", err)
	}
}
