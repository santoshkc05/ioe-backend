// Package templates renders notification emails from embedded Go templates.
package templates

import (
	"bytes"
	"embed"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

//go:embed welcome.txt.tmpl welcome.html.tmpl
var files embed.FS

const welcomeSubject = "Welcome to IOE"

// Renderer renders every email. It is safe for concurrent use.
type Renderer struct {
	welcomeText *texttemplate.Template
	welcomeHTML *htmltemplate.Template
}

// New parses the embedded templates.
func New() (*Renderer, error) {
	text, err := texttemplate.ParseFS(files, "welcome.txt.tmpl")
	if err != nil {
		return nil, err
	}
	html, err := htmltemplate.ParseFS(files, "welcome.html.tmpl")
	if err != nil {
		return nil, err
	}
	return &Renderer{welcomeText: text, welcomeHTML: html}, nil
}

type welcomeData struct{ Name string }

// Welcome renders the welcome email. A blank name is omitted from the greeting.
func (r *Renderer) Welcome(name string) (subject, text, html string, err error) {
	data := welcomeData{Name: strings.TrimSpace(name)}
	var textBuf, htmlBuf bytes.Buffer
	if err = r.welcomeText.Execute(&textBuf, data); err != nil {
		return "", "", "", err
	}
	if err = r.welcomeHTML.Execute(&htmlBuf, data); err != nil {
		return "", "", "", err
	}
	return welcomeSubject, textBuf.String(), htmlBuf.String(), nil
}
