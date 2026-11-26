//go:build !darwin

package worker

import "github.com/mainplane-ai/mainplane/pkg/release"

// Upgrade makes exe release v's and returns the path to run it from.
func Upgrade(exe, v string) (string, error) { return exe, release.Install(exe, "mainplane", v) }

// Rebundle is for macOS, where the service runs from an app bundle.
func Rebundle() {}
