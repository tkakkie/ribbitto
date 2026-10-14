// Package tablecheck enforces SQL table ownership from the module manifest
// and separate production/migration expression policies, including operators
// in comparisons, quantified subqueries and sort clauses.
// It uses tools/internal/sqlwalk for query loading and AST traversal, while keeping
// ownership, migration completeness and query read exemptions separate from
// scopecheck. Migration Up statements fail closed; backfills and trigger-bound
// routine SQL share the ownership walk, with reviewed read/write exemptions only
// after the owning module is settled. Unbound routines and routine calls fail.
// Foreign-key actions that write rows across module owners fail without exemptions.
package tablecheck
