// Package tablecheck enforces direct SQL table ownership from the module manifest.
// It uses tools/internal/sqlwalk for query loading and AST traversal, while keeping
// ownership, migration completeness and read exemptions separate from scopecheck.
package tablecheck
