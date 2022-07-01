package main

const (
	// ExitCodeSuccess is the exit code when the exporter exits without errors.
	ExitCodeSuccess = iota

	// ExitCodeStartupError is the exit code when the exporter fails to start correctly.
	ExitCodeStartupError
)
