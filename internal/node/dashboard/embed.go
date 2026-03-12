package dashboard

import (
	"embed"
	"html/template"
)

//go:embed templates/*.html static/css/*.css static/js/*.js static/img/*.svg static/img/*.png
var Content embed.FS

// GetTemplate returns the parsed dashboard template
func GetTemplate() (*template.Template, error) {
	return template.ParseFS(Content, "templates/index.html")
}

// GetHTML returns the raw dashboard HTML content
func GetHTML() (string, error) {
	data, err := Content.ReadFile("templates/index.html")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
