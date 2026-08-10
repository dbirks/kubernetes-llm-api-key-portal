package httpapp

import (
	"net/http"
)

// renderError writes a user-facing error page.
//
// The heading and message are always written by us, never derived from an
// internal error, so no stack trace, Kubernetes object, or OIDC error body can
// reach the browser. The request ID is included so a user reporting a problem
// can be matched to a log line.
func (a *App) renderError(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	noStore(w)

	data := ErrorPage{
		Page:    a.page(w, r, heading),
		Heading: heading,
		Message: message,
		Status:  status,
	}
	if err := a.renderer.render(w, status, "error.html", data); err != nil {
		// Rendering the error page is the last thing that can fail, so fall
		// back to plain text rather than recursing.
		a.log.Error("rendering error page failed",
			"request_id", RequestIDFrom(r.Context()), "error", err)
		writePlain(w, status, heading+". "+message)
	}
}

// mustRender renders a page, falling back to a generic error page if the
// template fails. A template failure is a programming error, so it is logged at
// error level with the request ID.
func (a *App) mustRender(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	if err := a.renderer.render(w, status, name, data); err != nil {
		a.log.Error("rendering page failed",
			"request_id", RequestIDFrom(r.Context()),
			"template", name, "error", err)
		a.renderError(w, r, http.StatusInternalServerError,
			"Something went wrong",
			"Nothing changed. Try again. If it keeps happening, ask the service owner.")
	}
}
