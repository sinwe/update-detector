//go:build !windows

package main

// Blank-imported solely for their init() side effects
// (checker.Register("ubuntu"/"debian", ...)) -- main.go itself never
// names these packages, which is what lets it compile identically
// regardless of GOOS. See docs/plugin-architecture-plan.md.
import (
	_ "update-detector/internal/checker/debian"
	_ "update-detector/internal/checker/ubuntu"
)
