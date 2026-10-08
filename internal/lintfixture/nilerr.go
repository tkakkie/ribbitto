//go:build lintfixture

package lintfixture

// SwallowedError must fail nilerr: a failed operation must not report success.
func SwallowedError(run func() error) error {
	if err := run(); err != nil {
		return nil
	}
	return nil
}
