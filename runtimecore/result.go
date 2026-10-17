package runtimecore

type Result struct {
	TaskID   int
	TaskName string
	WorkerID int

	Stdout   string
	Stderr   string
	ExitCode int

	Attempts []Attempt

	State State

	ErrorKind ErrorKind
	Err       error
}
