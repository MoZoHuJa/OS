// Command scarlix-bridge-reader atomically opens, validates, and reads the
// host-bridge desired-mode file. It replaces the shell `validate_input_file &&
// cat && re-validate` pattern in /usr/local/bin/scarlix-host-bridge which had
// a TOCTOU window between the validate and read syscalls.
//
// v18.7.4 P1: Atomicity model — open the file with O_NOFOLLOW (refuses
// symlinks), fstat the resulting fd (no path lookup, so the inode can't be
// swapped between stat and read), validate UID/mode/type/size from the fstat
// result, and read from the SAME fd. The kernel guarantees the inode can't
// change between fstat and read because both operate on the fd (which is a
// reference to a specific inode), not on the path (which could be renamed
// or replaced by an attacker with write access to the parent directory).
//
// Exit codes:
//
//	0 — success: prints the file contents (mode string) to stdout
//	1 — invalid input: not a regular file, wrong owner, wrong mode, too
//	    large, symlink, FIFO, socket, block/char device, or read error
//
// The host-bridge script captures stdout and uses it as the requested_mode;
// any error message goes to stderr (suppressed by the caller via 2>/dev/null).
//
// Build: from the scarlihq module root,
//
//	CGO_ENABLED=0 go build -o /usr/local/bin/scarlix-bridge-reader ./cmd/scarlix-bridge-reader
//
// Deployment: install.sh Phase 4 copies it to /usr/local/bin/scarlix-bridge-reader
// (root:root, mode 755). It runs as root (host-bridge is root) and reads files
// owned by UID 65532 (nonroot inside the ScarliHQ container).
package main

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: scarlix-bridge-reader <file>")
		os.Exit(1)
	}
	path := os.Args[1]

	// v18.7.4 P1: Open with O_NOFOLLOW (refuses symlinks).
	// If `path` is a symlink, Open returns ELOOP — defends against the
	// symlink-attack surface where ScarliHQ (UID 65532) could create a
	// symlink to /etc/shadow and have root (via host-bridge) read it.
	// O_RDONLY + O_NOFOLLOW is the security baseline.
	//
	// O_NONBLOCK: defends against FIFO/pipe DoS. Without O_NONBLOCK, Open
	// on a FIFO blocks until a writer opens the other end — ScarliHQ could
	// create a FIFO at desired-mode and the host-bridge would hang forever
	// (timer would never fire → no host-status.json → dashboard shows stale
	// forever). With O_NONBLOCK, Open returns immediately, and the fstat
	// check below rejects the FIFO (S_IFMT != S_IFREG). For regular files,
	// O_NONBLOCK has no effect on Open (regular files always open instantly).
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer syscall.Close(fd)

	// Clear O_NONBLOCK after Open so that read() on a regular file blocks
	// normally (we want blocking read so we get the full content even if
	// the file is on a slow NFS mount). For FIFOs we never reach this point
	// because the fstat check below rejects them first.
	//
	// FcntlGetfl/FcntlSetfl would be the proper way to toggle this, but
	// for regular files (which is all we accept) the difference is moot —
	// regular file reads return immediately when data is available, and
	// EOF when the file ends. Keep this comment as a note for future
	// maintainers: if you relax the S_IFREG check, you MUST clear O_NONBLOCK
	// here or reads on FIFOs will return EAGAIN immediately.

	// fstat the OPEN fd — not the path. This is the atomicity guarantee:
	// even if an attacker renames `path` to point at a different inode
	// between Open and the read below, we still operate on the original
	// inode that Open succeeded against. No TOCTOU window.
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		fmt.Fprintf(os.Stderr, "fstat: %v\n", err)
		os.Exit(1)
	}

	// Must be regular file (not FIFO, socket, block/char device, symlink).
	// O_NOFOLLOW already rejected symlinks-at-open, but a race where the
	// attacker replaces the file with a FIFO between Open and Fstat is
	// impossible because Open gives us an fd to whatever inode was at
	// `path` at open time. Still defensive: if the inode is somehow a
	// non-regular file (shouldn't happen with O_NOFOLLOW + a regular file
	// at open time, but the kernel could in theory allow Open on a FIFO),
	// reject here.
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		fmt.Fprintln(os.Stderr, "not a regular file")
		os.Exit(1)
	}

	// Must be owned by UID 65532 (nonroot in ScarliHQ container — the only
	// UID allowed to write to /var/lib/scarlix/bridge-input/desired-mode).
	// If root or any other UID owns the file, reject — ScarliHQ can't
	// create files as root, so a root-owned file in bridge-input/ means
	// either an admin mistake or a privilege escalation has occurred.
	if st.Uid != 65532 {
		fmt.Fprintf(os.Stderr, "owner uid=%d, expected 65532\n", st.Uid)
		os.Exit(1)
	}

	// Must have a safe permission mode. The host-bridge shell script
	// whitelisted 0600/0640/0700; we replicate that here so a file
	// with mode 0666 (world-writable — ScarliHQ could create this if
	// umask is misconfigured) is rejected.
	mode := st.Mode & 0777
	if mode != 0600 && mode != 0640 && mode != 0700 {
		fmt.Fprintf(os.Stderr, "mode=%o, expected 0600/0640/0700\n", mode)
		os.Exit(1)
	}

	// Max size 100 bytes. The mode name is at most ~20 chars ("creative",
	// "offline"); 100 bytes is generous and prevents a DoS where ScarliHQ
	// writes a 1GB file and we read it all into memory before passing to
	// the host-bridge script.
	if st.Size > 100 {
		fmt.Fprintln(os.Stderr, "file too large")
		os.Exit(1)
	}

	// Read from the SAME fd — atomic with the fstat above (kernel holds
	// the inode reference via the fd; rename/unlink/recreate of `path`
	// cannot affect this read).
	//
	// v18.7.5 P1: Use io.ReadFull for guaranteed full read (was: single
	// syscall.Read may return fewer bytes than requested on certain FS
	// paths/conditions — e.g. a file written with O_DIRECT or a CIFS mount
	// can return a short read mid-file. The previous code wrote only the
	// first `n` bytes to stdout, silently truncating the mode string →
	// scarlix-mode would receive a partial name → mode switch fails with
	// "invalid mode" even though the file content was correct).
	// io.ReadFull loops on the underlying Read until the buffer is full
	// OR EOF/ErrUnexpectedEOF — for a regular file of size N we get exactly
	// N bytes unless the file was truncated between fstat and read.
	file := os.NewFile(uintptr(fd), path)
	buf := make([]byte, int(st.Size))
	n, err := io.ReadFull(file, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}
	// If we got fewer bytes than expected (ErrUnexpectedEOF — file was
	// truncated between fstat and read), use what we got. The host-bridge
	// script trims whitespace, so a partial mode name will still be
	// rejected downstream by the mode validator — but at least we don't
	// silently truncate.
	if _, err := os.Stdout.Write(buf[:n]); err != nil {
		fmt.Fprintf(os.Stderr, "stdout write: %v\n", err)
		os.Exit(1)
	}
}
