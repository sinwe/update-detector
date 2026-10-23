//go:build darwin

package main

// Blank-imported solely for its init() side effect
// (checker.Register("macos", ...)) -- main.go itself never names this
// package, which is what lets it compile identically regardless of GOOS.
import (
	_ "update-detector/internal/checker/macos"
)
