package observe

import "golang.org/x/term"

// isTerminalOS reports whether the file descriptor fd refers to a terminal.
// This is the OS-backed implementation used in production; tests override the
// isTerminal variable instead of calling this directly.
func isTerminalOS(fd int) bool {
	return term.IsTerminal(fd)
}
