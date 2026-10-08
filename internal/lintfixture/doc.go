// Package lintfixture holds code that exists only for make check's lint
// fixtures (built with the lintfixture tag): the root proves staticcheck
// rejects ignored results in handwritten Go and generated templ output,
// bodyclose rejects unclosed HTTP response bodies, and sqlclosecheck rejects
// unused, unclosed pgx rows. Any other rows use, such as rows.Next() or
// rows.Err(), satisfies sqlclosecheck: keep defer rows.Close(), since lint
// does not catch a missing Close once rows are used. Subpackages show that
// depguard rejects or accepts an import. Normal builds see only doc comments.
package lintfixture
