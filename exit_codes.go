package main

const (
	// ExitCodeOK is the exit code when the exporter exits without errors.
	ExitCodeOK = iota

	// ExitCodeStartupError is the exit code when the exporter fails to start correctly.
	ExitCodeStartupError
)
