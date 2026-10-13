package session

import "syscall"

// sigZero probes a process without affecting it.
var sigZero = syscall.Signal(0)
