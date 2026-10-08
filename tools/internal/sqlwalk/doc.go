// Package sqlwalk loads named production queries and walks PostgreSQL JSON ASTs
// for the SQL gates. It owns parsing, node access, statement traversal and CTE
// visibility; callers own node acceptance, organisation scope and table ownership.
// Traversal options preserve scopecheck's collected CTE names and opaque INSERT
// bodies, while tablecheck uses sequential, lexical CTE scopes and physical writes.
package sqlwalk
