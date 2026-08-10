package httpapp

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"
)

// Template tree layout. Pages are composed with the shared layout and partials
// rather than sharing one flat template set, so that every page can define
// blocks with the same names ("title", "main") without colliding.
const (
	layoutGlob   = "templates/base.html"
	partialsGlob = "templates/partials/*.html"
	pagesGlob    = "templates/pages/*.html"
)

// renderer compiles and executes the page templates.
//
// In production the templates come from an embedded filesystem and are parsed
// once at startup. In development they are re-parsed per request so an edit
// shows up on refresh, which is what makes UI iteration tolerable.
type renderer struct {
	fsys   fs.FS
	reload bool

	cached map[string]*template.Template
}

func newRenderer(fsys fs.FS, reload bool) (*renderer, error) {
	r := &renderer{fsys: fsys, reload: reload}
	// Parse once up front either way, so a broken template fails at startup
	// rather than on the first request that happens to use it.
	pages, err := r.parse()
	if err != nil {
		return nil, err
	}
	if !reload {
		r.cached = pages
	}
	return r, nil
}

// parse builds one template set per page: layout + partials + that page.
func (r *renderer) parse() (map[string]*template.Template, error) {
	base, err := template.New("base.html").Funcs(templateFuncs).ParseFS(r.fsys, layoutGlob)
	if err != nil {
		return nil, fmt.Errorf("parse layout: %w", err)
	}
	// Partials are optional; a tree without any is valid.
	if matches, _ := fs.Glob(r.fsys, partialsGlob); len(matches) > 0 {
		if base, err = base.ParseFS(r.fsys, partialsGlob); err != nil {
			return nil, fmt.Errorf("parse partials: %w", err)
		}
	}

	pagePaths, err := fs.Glob(r.fsys, pagesGlob)
	if err != nil {
		return nil, fmt.Errorf("find pages: %w", err)
	}
	if len(pagePaths) == 0 {
		return nil, fmt.Errorf("no page templates found under %s", pagesGlob)
	}

	pages := make(map[string]*template.Template, len(pagePaths))
	for _, p := range pagePaths {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone layout for %s: %w", p, err)
		}
		if _, err := clone.ParseFS(r.fsys, p); err != nil {
			return nil, fmt.Errorf("parse page %s: %w", p, err)
		}
		pages[path.Base(p)] = clone
	}
	return pages, nil
}

func (r *renderer) lookup(name string) (*template.Template, error) {
	pages := r.cached
	if r.reload {
		parsed, err := r.parse()
		if err != nil {
			return nil, err
		}
		pages = parsed
	}
	tpl, ok := pages[name]
	if !ok {
		return nil, fmt.Errorf("no such page template: %s", name)
	}
	return tpl, nil
}

// render writes a page.
//
// The template is executed into a buffer first, so a failure partway through
// produces a clean error response instead of a half-written page with a 200
// already committed.
func (r *renderer) render(w http.ResponseWriter, status int, name string, data any) error {
	tpl, err := r.lookup(name)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	// Execution starts at the layout; the page contributes the blocks it
	// overrides.
	if err := tpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err = buf.WriteTo(w)
	return err
}

// templateFuncs are the helpers available to templates. The set is kept small
// on purpose: logic belongs in Go, where it can be tested.
var templateFuncs = template.FuncMap{
	// date formats a timestamp for display, e.g. "Aug 9, 2026".
	"date": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("Jan 2, 2006")
	},
	// datetime is the machine-readable form for a <time datetime="..."> attribute.
	"datetime": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	},
}

func splitName(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '.'
	})
}

func upperFirst(s string) string {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
	}
	return ""
}
