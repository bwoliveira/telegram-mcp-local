package platform

// The programs the app runs are stopped with care: an interrupt first,
// allowing the child to close its resources, and a kill only when that is
// ignored. What "interrupt" means differs:
//
//	Unix:    SIGINT to the child's process group, which Prepare creates.
//	Windows: CTRL_BREAK to the child's console process group, which a Go
//	         program receives as os.Interrupt. The child also runs inside a
//	         job object that closes with the app, so it cannot outlive it.
//
// Prepare must be called before cmd.Start and Started right after it.
