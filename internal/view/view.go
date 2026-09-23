package view

import (
	"html/template"
	"net/http"
	"os"
	"path/filepath"
)

type View struct {
	baseDir string
}

func New() (*View, error) {
	baseDir := "web/templates"
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		baseDir = "/app/web/templates"
	}
	return &View{baseDir: baseDir}, nil
}

// Render assembles and executes a template.
// For partials (session-list.html, file-list.html) it renders directly.
// For pages it wraps with layouts/base.html by parsing both together per-request,
// avoiding {{define}} name collisions across pages.
func (v *View) Render(w http.ResponseWriter, name string, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if data == nil {
		data = map[string]any{}
	}

	// Partials live in partials/ and are rendered without a layout.
	partialPath := filepath.Join(v.baseDir, "partials", name)
	if _, err := os.Stat(partialPath); err == nil {
		tmpl, err := template.ParseFiles(partialPath)
		if err != nil {
			return err
		}
		return tmpl.Execute(w, data)
	}

	// Pages live in pages/ and are wrapped with layouts/base.html.
	pagePath := filepath.Join(v.baseDir, "pages", name)
	layoutPath := filepath.Join(v.baseDir, "layouts", "base.html")

	tmpl, err := template.ParseFiles(layoutPath, pagePath)
	if err != nil {
		return err
	}
	return tmpl.ExecuteTemplate(w, "base.html", data)
}
