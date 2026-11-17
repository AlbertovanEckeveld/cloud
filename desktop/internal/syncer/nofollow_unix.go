//go:build unix

package syncer

import "syscall"

// noFollow makes opening fail if the final path element is a symlink, so a
// file swapped for a link between scanning and opening is never followed.
const noFollow = syscall.O_NOFOLLOW
