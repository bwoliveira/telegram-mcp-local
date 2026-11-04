package platform

import "syscall"

// setParentDeathSignal makes the kernel send SIGTERM to the child when the app
// dies, however it dies: the child then closes its resources as on any stop.
func setParentDeathSignal(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGTERM
}
