//go:build !darwin && !linux

package state

// Operation locks need flock(2), which this package supports on macOS and
// Linux only. There is deliberately no unlocked fallback: without the lock,
// garbage collection could delete an artifact between its publication and the
// database commit that references it. Building for another platform stops here.
var _ = state_operation_locks_are_supported_on_darwin_and_linux_only
