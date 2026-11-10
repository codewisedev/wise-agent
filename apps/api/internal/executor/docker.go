package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Docker runs sandboxes as Docker containers (through the socket proxy).
type Docker struct {
	cli       *client.Client
	volume    string // the workspaces volume; sandboxes mount subpaths of it
	egress    string // label value of the egress proxy container (wise.role=egress)
	cliVolume string // the coding CLI bundle volume (runners mount a subpath read-only)
}

// SetCLIVolume names the coding CLI bundle volume.
func (d *Docker) SetCLIVolume(v string) { d.cliVolume = v }

// NewDocker connects to the Docker API at host (e.g. tcp://wise-docker-proxy:2375).
func NewDocker(host, volume string) (*Docker, error) {
	cli, err := client.New(client.WithHost(host), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Docker{cli: cli, volume: volume, egress: "egress"}, nil
}

func name(id string) string { return "wise-sbx-" + id }

// Ping checks the Docker API answers.
func (d *Docker) Ping(ctx context.Context) error {
	_, err := d.cli.Ping(ctx, client.PingOptions{})
	return err
}

func (d *Docker) ImageExists(ctx context.Context, image string) (bool, error) {
	_, err := d.cli.ImageInspect(ctx, image)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (d *Docker) EnsureImage(ctx context.Context, image string, log io.Writer) error {
	if ok, err := d.ImageExists(ctx, image); err != nil || ok {
		return err
	}
	fmt.Fprintf(log, "pulling %s …\n", image)
	rc, err := d.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w (build or pull the image on the host, then retry)", image, err)
	}
	defer rc.Close()
	if err := rc.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	fmt.Fprintf(log, "pulled %s\n", image)
	return nil
}

// egressContainer finds the egress proxy container by label.
func (d *Docker) egressContainer(ctx context.Context) (string, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		Filters: make(client.Filters).Add("label", "wise.role="+d.egress),
	})
	if err != nil {
		return "", err
	}
	for _, c := range res.Items {
		return c.ID, nil
	}
	return "", errors.New("the egress proxy (wise-egress) is not running")
}

// network creates the sandbox's private network (no route out except the
// egress proxy, which joins it) unless it exists.
func (d *Docker) network(ctx context.Context, id string) error {
	n := name(id)
	labels := map[string]string{"wise.workspace": id, "wise.role": "sandbox"}
	if _, err := d.cli.NetworkCreate(ctx, n, client.NetworkCreateOptions{Driver: "bridge", Internal: true, Labels: labels}); err != nil && !cerrdefs.IsConflict(err) {
		return fmt.Errorf("create network: %w", err)
	}
	eg, err := d.egressContainer(ctx)
	if err != nil {
		return err
	}
	if _, err := d.cli.NetworkConnect(ctx, n, client.NetworkConnectOptions{
		Container: eg, EndpointConfig: &network.EndpointSettings{Aliases: []string{"wise-egress"}},
	}); err != nil && !cerrdefs.IsConflict(err) && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("connect egress proxy: %w", err)
	}
	return nil
}

// attach connects the service labelled wise.role=<role> to a sandbox network
// under alias (idempotent).
func (d *Docker) attach(ctx context.Context, id, role, alias string) error {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{Filters: make(client.Filters).Add("label", "wise.role="+role)})
	if err != nil {
		return err
	}
	if len(res.Items) == 0 {
		return fmt.Errorf("%s is not running", alias)
	}
	if _, err := d.cli.NetworkConnect(ctx, name(id), client.NetworkConnectOptions{
		Container: res.Items[0].ID, EndpointConfig: &network.EndpointSettings{Aliases: []string{alias}},
	}); err != nil && !cerrdefs.IsConflict(err) && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("connect %s: %w", alias, err)
	}
	return nil
}

// dropNetwork removes the sandbox's network (stopped sandboxes keep none, so
// retained workspaces do not use up Docker's address pools).
func (d *Docker) dropNetwork(ctx context.Context, id string) error {
	n := name(id)
	if eg, err := d.egressContainer(ctx); err == nil {
		_, _ = d.cli.NetworkDisconnect(ctx, n, client.NetworkDisconnectOptions{Container: eg, Force: true})
	}
	_, _ = d.cli.NetworkDisconnect(ctx, n, client.NetworkDisconnectOptions{Container: n, Force: true})
	if res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{Filters: make(client.Filters).Add("label", "wise.role=model-proxy")}); err == nil {
		for _, c := range res.Items {
			_, _ = d.cli.NetworkDisconnect(ctx, n, client.NetworkDisconnectOptions{Container: c.ID, Force: true})
		}
	}
	if _, err := d.cli.NetworkRemove(ctx, n, client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

func (d *Docker) Create(ctx context.Context, spec SandboxSpec) error {
	n := name(spec.ID)
	labels := map[string]string{"wise.workspace": spec.ID, "wise.role": "sandbox"}
	if err := d.network(ctx, spec.ID); err != nil {
		return err
	}
	var err error
	sub := func(p, target string, ro bool) mount.Mount {
		return mount.Mount{Type: mount.TypeVolume, Source: d.volume, Target: target, ReadOnly: ro,
			VolumeOptions: &mount.VolumeOptions{NoCopy: true, Subpath: spec.ID + "/" + p}}
	}
	pids := spec.Pids
	_, err = d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: n,
		Config: &container.Config{
			Image: spec.Image, User: spec.User, Env: spec.Env, WorkingDir: "/workspace", Labels: labels,
			Entrypoint: []string{"/opt/wise/wise-sandbox"}, Cmd: []string{"idle"},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode(n),
			Mounts:         []mount.Mount{sub("repo", "/workspace", false), sub("home", "/home/wise", false), sub("tools", "/opt/wise", true)},
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			ReadonlyRootfs: true,
			// exec: test runners build and run binaries in /tmp (go test, cargo, node-gyp).
			Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,exec,size=1g"},
			Init:  ptr(true),
			Resources: container.Resources{
				NanoCPUs: int64(spec.CPUs * 1e9), Memory: spec.MemoryMB << 20, PidsLimit: &pids,
			},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		},
	})
	if err != nil && !cerrdefs.IsConflict(err) {
		return fmt.Errorf("create sandbox: %w", err)
	}
	return d.Ensure(ctx, spec.ID)
}

func ptr[T any](v T) *T { return &v }

func (d *Docker) Ensure(ctx context.Context, id string) error {
	st, err := d.State(ctx, id)
	if err != nil {
		return err
	}
	switch st {
	case "running":
		return nil
	case "missing":
		return errors.New("the sandbox no longer exists; prepare the workspace again")
	}
	// A stopped sandbox has no network: recreate it and attach the container.
	if err := d.network(ctx, id); err != nil {
		return err
	}
	if _, err := d.cli.NetworkConnect(ctx, name(id), client.NetworkConnectOptions{Container: name(id)}); err != nil &&
		!cerrdefs.IsConflict(err) && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("attach sandbox network: %w", err)
	}
	if _, err := d.cli.ContainerStart(ctx, name(id), client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start sandbox: %w", err)
	}
	return nil
}

func (d *Docker) Stop(ctx context.Context, id string) error {
	t := 5
	_, err := d.cli.ContainerStop(ctx, name(id), client.ContainerStopOptions{Timeout: &t})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return d.dropNetwork(ctx, id)
}

func (d *Docker) Remove(ctx context.Context, id string) error {
	if _, err := d.cli.ContainerRemove(ctx, name(id), client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return d.dropNetwork(ctx, id)
}

func (d *Docker) State(ctx context.Context, id string) (string, error) {
	res, err := d.cli.ContainerInspect(ctx, name(id), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if res.Container.State != nil && res.Container.State.Running {
		return "running", nil
	}
	return "stopped", nil
}

func runnerName(runID string) string { return "wise-run-" + runID }

func (d *Docker) CreateRunner(ctx context.Context, spec RunnerSpec) error {
	if d.cliVolume == "" {
		return errors.New("the coding CLI bundle is not configured (WISE_CODING_CLIS_VOLUME)")
	}
	if err := d.network(ctx, spec.WorkspaceID); err != nil {
		return err
	}
	if spec.ModelProxy {
		if err := d.attach(ctx, spec.WorkspaceID, "model-proxy", "wise-model-proxy"); err != nil {
			return err
		}
	}
	n := runnerName(spec.RunID)
	labels := map[string]string{"wise.workspace": spec.WorkspaceID, "wise.role": "runner", "wise.run": spec.RunID}
	vol := func(src, sub, target string, ro bool) mount.Mount {
		return mount.Mount{Type: mount.TypeVolume, Source: src, Target: target, ReadOnly: ro,
			VolumeOptions: &mount.VolumeOptions{NoCopy: true, Subpath: sub}}
	}
	pids := spec.Pids
	_, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: n,
		Config: &container.Config{
			Image: spec.Image, User: spec.User, Env: spec.Env, WorkingDir: "/workspace", Labels: labels,
			Entrypoint: []string{"/opt/wise/wise-sandbox"}, Cmd: []string{"idle"},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(name(spec.WorkspaceID)),
			Mounts: []mount.Mount{
				vol(d.volume, spec.WorkspaceID+"/repo", "/workspace", false),
				// The CLI never sees git metadata: an empty tmpfs hides .git.
				{Type: mount.TypeTmpfs, Target: "/workspace/.git", TmpfsOptions: &mount.TmpfsOptions{SizeBytes: 1 << 20, Mode: 0o500}},
				vol(d.volume, spec.WorkspaceID+"/runs/"+spec.RunID+"/home", "/home/wise", false),
				vol(d.volume, spec.WorkspaceID+"/tools", "/opt/wise", true),
				vol(d.cliVolume, spec.CLISubpath, "/opt/cli", true),
			},
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			ReadonlyRootfs: true,
			Tmpfs:          map[string]string{"/tmp": "rw,nosuid,nodev,exec,size=1g"},
			Init:           ptr(true),
			Resources: container.Resources{
				NanoCPUs: int64(spec.CPUs * 1e9), Memory: spec.MemoryMB << 20, PidsLimit: &pids,
			},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		},
	})
	if err != nil {
		return fmt.Errorf("create runner: %w", err)
	}
	if _, err := d.cli.ContainerStart(ctx, n, client.ContainerStartOptions{}); err != nil {
		_ = d.RemoveRunner(context.WithoutCancel(ctx), spec.RunID)
		return fmt.Errorf("start runner: %w", err)
	}
	return nil
}

func (d *Docker) ExecRunner(ctx context.Context, runID string, cmd []string, env []string, stdin io.Reader, out io.Writer) (int, error) {
	return d.execIn(ctx, runnerName(runID), cmd, env, stdin, out)
}

func (d *Docker) RemoveRunner(ctx context.Context, runID string) error {
	if _, err := d.cli.ContainerRemove(ctx, runnerName(runID), client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

func (d *Docker) Exec(ctx context.Context, id string, cmd []string, env []string, stdin io.Reader, out io.Writer) (int, error) {
	return d.execIn(ctx, name(id), cmd, env, stdin, out)
}

func (d *Docker) execIn(ctx context.Context, container string, cmd []string, env []string, stdin io.Reader, out io.Writer) (int, error) {
	ex, err := d.cli.ExecCreate(ctx, container, client.ExecCreateOptions{
		Cmd: cmd, Env: env, AttachStdout: true, AttachStderr: true, AttachStdin: stdin != nil, WorkingDir: "/workspace",
	})
	if err != nil {
		return -1, fmt.Errorf("exec: %w", err)
	}
	att, err := d.cli.ExecAttach(ctx, ex.ID, client.ExecAttachOptions{})
	if err != nil {
		return -1, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()
	if stdin != nil {
		go func() {
			_, _ = io.Copy(att.Conn, stdin)
			if cw, ok := att.Conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}()
	}
	copyErr := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(out, out, att.Reader)
		copyErr <- err
	}()
	select {
	case <-ctx.Done():
		att.Close()
		return -1, ctx.Err()
	case err := <-copyErr:
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintf(out, "\n[wise] output stream interrupted: %v\n", err)
		}
	}
	// The stream may end slightly before the process is reaped.
	for i := 0; ; i++ {
		ins, err := d.cli.ExecInspect(ctx, ex.ID, client.ExecInspectOptions{})
		if err != nil {
			return -1, err
		}
		if !ins.Running {
			return ins.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(time.Duration(min(100*(i+1), 2000)) * time.Millisecond):
		}
	}
}
