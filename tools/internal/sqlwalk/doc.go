// Package sqlwalk loads named production queries and walks PostgreSQL JSON ASTs
// for the SQL gates. It owns parsing, node access, statement traversal and CTE
// visibility and exemption-reason validation. BigintLocations provides token
// locations for tablecheck's type policy; callers own node acceptance,
// organisation scope and table ownership.
//
// Options preserve scopecheck's collected CTE names and opaque INSERT bodies,
// while tablecheck uses sequential, lexical CTE scopes. Walk always passes write
// targets to Relation, even when a visible CTE has the same name. ReasonRules
// preserves scopecheck's colon-terminated pending marker and issue-comment syntax;
// tablecheck also rejects bare markers and accepts review-comment provenance.
package sqlwalk
