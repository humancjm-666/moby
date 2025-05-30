package executor

import (
	"context"
	"io"
	"net"
	"syscall"

	"github.com/containerd/containerd/v2/core/mount"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/sys/user"
)

type Meta struct {
	Args           []string
	Env            []string
	User           string
	Cwd            string
	Hostname       string
	Tty            bool
	ReadonlyRootFS bool
	ExtraHosts     []HostIP
	Ulimit         []*pb.Ulimit
	CDIDevices     []*pb.CDIDevice
	CgroupParent   string
	NetMode        pb.NetMode
	SecurityMode   pb.SecurityMode
	ValidExitCodes []int

	RemoveMountStubsRecursive bool
}

type MountableRef interface {
	Mount() ([]mount.Mount, func() error, error)
	IdentityMapping() *user.IdentityMapping
}

type Mountable interface {
	Mount(ctx context.Context, readonly bool) (MountableRef, error)
}

type Mount struct {
	Src      Mountable
	Selector string
	Dest     string
	Readonly bool
}

type WinSize struct {
	Rows uint32
	Cols uint32
}

type ProcessInfo struct {
	Meta           Meta
	Stdin          io.ReadCloser
	Stdout, Stderr io.WriteCloser
	Resize         <-chan WinSize
	Signal         <-chan syscall.Signal
}

type Executor interface {
	// Run will start a container for the given process with rootfs, mounts.
	// `id` is an optional name for the container so it can be referenced later via Exec.
	// `started` is an optional channel that will be closed when the container setup completes and has started running.
	Run(ctx context.Context, id string, rootfs Mount, mounts []Mount, process ProcessInfo, started chan<- struct{}) (resourcestypes.Recorder, error)
	// Exec will start a process in container matching `id`. An error will be returned
	// if the container failed to start (via Run) or has exited before Exec is called.
	Exec(ctx context.Context, id string, process ProcessInfo) error
}

type HostIP struct {
	Host string
	IP   net.IP
}
