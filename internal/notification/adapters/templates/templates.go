// Package templates renders notification emails from embedded Go templates.
package templates

import (
	"bytes"
	"embed"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"

	"github.com/santoshkc2200/ioe-backend/internal/notification/app"
)

//go:embed welcome.txt.tmpl welcome.html.tmpl purchase_paid.txt.tmpl purchase_paid.html.tmpl purchase_refunded.txt.tmpl purchase_refunded.html.tmpl
var files embed.FS

const (
	welcomeSubject  = "Welcome to IOE"
	paidSubject     = "Payment received"
	refundedSubject = "Refund issued"
)

// Renderer renders every email. It is safe for concurrent use.
type Renderer struct {
	welcomeText  *texttemplate.Template
	welcomeHTML  *htmltemplate.Template
	paidText     *texttemplate.Template
	paidHTML     *htmltemplate.Template
	refundedText *texttemplate.Template
	refundedHTML *htmltemplate.Template
}

// New parses the embedded templates.
func New() (*Renderer, error) {
	r := &Renderer{}
	var err error
	if r.welcomeText, err = texttemplate.ParseFS(files, "welcome.txt.tmpl"); err != nil {
		return nil, err
	}
	if r.welcomeHTML, err = htmltemplate.ParseFS(files, "welcome.html.tmpl"); err != nil {
		return nil, err
	}
	if r.paidText, err = texttemplate.ParseFS(files, "purchase_paid.txt.tmpl"); err != nil {
		return nil, err
	}
	if r.paidHTML, err = htmltemplate.ParseFS(files, "purchase_paid.html.tmpl"); err != nil {
		return nil, err
	}
	if r.refundedText, err = texttemplate.ParseFS(files, "purchase_refunded.txt.tmpl"); err != nil {
		return nil, err
	}
	if r.refundedHTML, err = htmltemplate.ParseFS(files, "purchase_refunded.html.tmpl"); err != nil {
		return nil, err
	}
	return r, nil
}

type welcomeData struct{ Name string }

// Welcome renders the welcome email. A blank name is omitted from the greeting.
func (r *Renderer) Welcome(name string) (subject, text, html string, err error) {
	text, html, err = execute(r.welcomeText, r.welcomeHTML, welcomeData{Name: strings.TrimSpace(name)})
	return welcomeSubject, text, html, err
}

// PurchasePaid renders the payment-received email. Blank name, title and link are omitted.
func (r *Renderer) PurchasePaid(e app.PurchasePaidEmail) (subject, text, html string, err error) {
	e.Name, e.CourseTitle = strings.TrimSpace(e.Name), strings.TrimSpace(e.CourseTitle)
	subject = paidSubject
	if e.CourseTitle != "" {
		subject += ": " + e.CourseTitle
	}
	text, html, err = execute(r.paidText, r.paidHTML, e)
	return subject, text, html, err
}

// PurchaseRefunded renders the refund-issued email. Blank name and title are omitted.
func (r *Renderer) PurchaseRefunded(e app.PurchaseRefundedEmail) (subject, text, html string, err error) {
	e.Name, e.CourseTitle = strings.TrimSpace(e.Name), strings.TrimSpace(e.CourseTitle)
	subject = refundedSubject
	if e.CourseTitle != "" {
		subject += ": " + e.CourseTitle
	}
	text, html, err = execute(r.refundedText, r.refundedHTML, e)
	return subject, text, html, err
}

func execute(t *texttemplate.Template, h *htmltemplate.Template, data any) (string, string, error) {
	var textBuf, htmlBuf bytes.Buffer
	if err := t.Execute(&textBuf, data); err != nil {
		return "", "", err
	}
	if err := h.Execute(&htmlBuf, data); err != nil {
		return "", "", err
	}
	return textBuf.String(), htmlBuf.String(), nil
}
