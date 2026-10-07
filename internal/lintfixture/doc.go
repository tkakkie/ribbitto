// Package lintfixture holds code that exists only for make check's lint
// fixtures (built with the lintfixture tag): the root proves staticcheck
// rejects ignored results in handwritten and generated code; subpackages
// show that depguard rejects or accepts an import. Normal builds see only
// doc comments.
package lintfixture
