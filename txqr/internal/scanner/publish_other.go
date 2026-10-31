//go:build !windows

package scanner

import "os"

// The test/portable implementation also refuses to replace existing files.
func publishExclusive(from, to string) error { return os.Link(from, to) }
