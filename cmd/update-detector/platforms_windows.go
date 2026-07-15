//go:build windows

package main

// No platform package to blank-import yet -- internal/checker/windows
// doesn't exist until Phase 2 of docs/plugin-architecture-plan.md. A
// windows build of this binary compiles fine as of Phase 1, but
// checker.New("windows", ...) fails at runtime ("no checker registered")
// until that package lands and this file blank-imports it.
