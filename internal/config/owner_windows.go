//go:build windows

package config

import (
	"io/fs"

	"github.com/youyo/focal/internal/result"
)

// checkConfigOwner has nothing to say on Windows, where a file's owner is an
// ACL question rather than a uid one and the mode bits the sibling check reads
// are already a Go approximation. Answering it wrongly here would be worse than
// leaving it to the permission check.
func checkConfigOwner(_ string, _ fs.FileInfo) *result.Error { return nil }
