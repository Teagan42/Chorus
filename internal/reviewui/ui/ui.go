// Package ui is the Chorus review UI kit: Go html/template partials, one CSS
// file per component, and the view-model types each partial expects.
//
// Every component lives in three files that share a name (Go files use
// underscores where the template and stylesheet use hyphens):
//
//	ui/<name>.go                          view model (the template's dot)
//	ui/templates/components/<name>.tmpl   {{define "<name>"}} partial
//	ui/static/css/components/<name>.css   styles, built on tokens.css
//
// Load once at startup, then execute pages or single partials (htmx swaps):
//
//	tpl := ui.MustTemplates()
//	mux.Handle("/static/", http.StripPrefix("/static/", ui.Static()))
//	tpl.ExecuteTemplate(w, "pair-actions", pa)
package ui

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed templates static
var assets embed.FS

// Templates parses every partial and layout in the kit. Add your own pages
// with tpl.ParseFS / tpl.ParseFiles on the returned template.
func Templates() (*template.Template, error) {
	return template.New("chorus").Funcs(Funcs()).ParseFS(assets,
		"templates/components/*.tmpl",
		"templates/layout/*.tmpl",
	)
}

// MustTemplates is Templates for use in main.
func MustTemplates() *template.Template {
	t, err := Templates()
	if err != nil {
		panic(err)
	}
	return t
}

// Static serves tokens.css, base.css, the component CSS, chorus.css (which
// imports all of them), the fonts, the small chorus.js and the brand images.
func Static() http.Handler {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Go's mime table has no fonts, and a sniffed font is octet-stream.
		if t := fontTypes[path.Ext(r.URL.Path)]; t != "" {
			w.Header().Set("Content-Type", t)
		}
		files.ServeHTTP(w, r)
	})
}

var fontTypes = map[string]string{".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf"}

// StaticFS exposes the same files, e.g. for copying into a build step.
func StaticFS() fs.FS {
	sub, _ := fs.Sub(assets, "static")
	return sub
}

// Funcs are the template helpers the partials rely on.
func Funcs() template.FuncMap {
	return template.FuncMap{
		// pct renders a 0–100 position as a CSS percentage.
		"pct": func(v float64) template.CSS { return template.CSS(fmt.Sprintf("%.3f%%", v)) },
		// px renders an integer as CSS pixels.
		"px": func(v int) template.CSS { return template.CSS(fmt.Sprintf("%dpx", v)) },
		// tone turns a Tone into its class name.
		"tone": func(t Tone) string { return t.Class() },
		// cls joins class names, skipping empty ones.
		"cls": func(parts ...string) string {
			out := parts[:0]
			for _, p := range parts {
				if p != "" {
					out = append(out, p)
				}
			}
			return strings.Join(out, " ")
		},
		// when returns s if cond is true, otherwise "".
		"when": func(cond bool, s string) string {
			if cond {
				return s
			}
			return ""
		},
		// dict builds a map for passing several values into a partial.
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of arguments")
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		"add":  func(a, b int) int { return a + b },
		"mulf": func(a, b float64) float64 { return a * b },
		"mul":  func(a, b int) int { return a * b },
		"bool": func(b bool) string { return fmt.Sprint(b) },
		// safeCSS passes a trusted CSS value through (used for grid track lists).
		"safeCSS": func(s string) template.CSS { return template.CSS(s) },
	}
}

// Hx carries optional htmx attributes for any interactive component.
// Leave a field empty to omit the attribute.
type Hx struct {
	Get     string
	Post    string
	Target  string
	Swap    string
	Include string
	Trigger string
	Vals    string // JSON, e.g. {"reason":"duplicate"}
	PushURL string
}
