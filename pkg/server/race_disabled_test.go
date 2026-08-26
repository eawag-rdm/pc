//go:build !race

package server

// raceDetectorEnabled lets a test opt out of assertions the race
// instrumentation invalidates, such as allocation counts.
const raceDetectorEnabled = false
