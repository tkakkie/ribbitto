//go:build lintfixture

package sub

import "github.com/tkakkie/ribbitto/internal/web"

// Services is a web dependency that composition code must not have.
type Services = web.Services
