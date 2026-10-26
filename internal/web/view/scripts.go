package view

import (
	"context"
	"io"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/web/static"
)

func pageScript(name string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		url, err := static.ScriptURL(ctx, name)
		if err != nil {
			return err
		}
		return scriptTag(url).Render(ctx, w)
	})
}
