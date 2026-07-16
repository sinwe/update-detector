//go:build windows

package main

// Blank-imported solely for its init() side effect
// (checker.Register("windows", ...)) -- main.go itself never names this
// package, which is what lets it compile identically regardless of GOOS.
// See docs/plugin-architecture-plan.md, Phase 2.
import (
	_ "update-detector/internal/checker/windows"
)
