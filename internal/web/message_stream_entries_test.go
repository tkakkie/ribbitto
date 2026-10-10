package web

import (
	"context"
	"html/template"
	"io"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
)

// streamEntriesTemplate renders the browser test's fake OOB entries. It is
// html/template rather than templ: a .templ file anywhere in the module would
// make `make generate` emit a non-test Go file for a test-only component.
var streamEntriesTemplate = template.Must(template.New("entries").Parse(
	`{{range .IDs}}<div lang="{{$.Lang}}" id="{{.}}" hx-swap-oob="outerHTML">{{$.State}}</div>{{end}}`))

// streamBrowserEntries supplies fake OOB entries only to the browser test.
func streamBrowserEntries(ids []string, state string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return streamEntriesTemplate.Execute(w, struct {
			IDs         []string
			Lang, State string
		}{ids, i18n.Language(ctx), state})
	})
}
