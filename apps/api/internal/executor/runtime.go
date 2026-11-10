package executor

import (
	"context"
	"io"
)

// SandboxSpec describes one task sandbox. The executor builds the container
// from this fixed shape only: no other mounts, never privileged, never on the
// host network.
type SandboxSpec struct {
	ID       string // workspace id
	Image    string
	User     string // uid:gid
	Env      []string
	CPUs     float64
	MemoryMB int64
	Pids     int64
}

// Runtime runs sandboxes. Docker in production; a local stand-in in tests.
type Runtime interface {
	// EnsureImage makes the image available (pulling it when missing).
	EnsureImage(ctx context.Context, image string, log io.Writer) error
	// ImageHas reports image metadata useful for preflight (exists locally).
	ImageExists(ctx context.Context, image string) (bool, error)
	// Create creates and starts the sandbox (its network, container).
	Create(ctx context.Context, spec SandboxSpec) error
	// Ensure starts a stopped sandbox; it fails when the sandbox is gone.
	Ensure(ctx context.Context, id string) error
	// Stop stops the sandbox, keeping its files.
	Stop(ctx context.Context, id string) error
	// Remove deletes the sandbox and its network (not the workspace files).
	Remove(ctx context.Context, id string) error
	// State is "running", "stopped" or "missing".
	State(ctx context.Context, id string) (string, error)
	// Exec runs cmd in the sandbox as its user. stdin may be nil. Output
	// (stdout and stderr together) goes to out. It returns the exit code.
	Exec(ctx context.Context, id string, cmd []string, env []string, stdin io.Reader, out io.Writer) (int, error)
}
