// Package compose shares cross-module adapters and composition between cmd/*
// and tests (decision 26, amended 2026-10-18). It owns no business logic,
// transactions or tables and is not a service layer or orchestration module.
// Only cmd/* and test files may import it.
package compose
