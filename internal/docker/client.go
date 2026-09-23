package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type Client struct {
	cli                *client.Client
	managerContainerID string
	networkMu          sync.Mutex
}

func NewClient() (*Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	managerID := os.Getenv("RB_MANAGER_CONTAINER_ID")
	if managerID == "" {
		if _, err := os.Stat("/.dockerenv"); err == nil {
			managerID, err = os.Hostname()
			if err != nil {
				return nil, fmt.Errorf("identify manager container: %w", err)
			}
		}
	}
	return &Client{cli: cli, managerContainerID: managerID}, nil
}

func (c *Client) Close() error {
	return c.cli.Close()
}

func (c *Client) CreateSessionContainer(ctx context.Context, opts CreateSessionOptions) (string, error) {
	exposedPorts := nat.PortSet{
		nat.Port(fmt.Sprintf("%s/tcp", opts.NoVNCPort)):        struct{}{},
		nat.Port(fmt.Sprintf("%s/tcp", opts.AudioServicePort)): struct{}{},
		nat.Port(fmt.Sprintf("%s/tcp", opts.WebRTCPort)):       struct{}{},
		nat.Port(fmt.Sprintf("%s/tcp", opts.InputServicePort)): struct{}{},
		nat.Port(fmt.Sprintf("%s/udp", opts.WebRTCUDPPort)):    struct{}{},
		nat.Port(fmt.Sprintf("%s/tcp", opts.FileServicePort)):  struct{}{},
	}

	portBindings := nat.PortMap{
		nat.Port(fmt.Sprintf("%s/udp", opts.WebRTCUDPPort)): []nat.PortBinding{{HostPort: opts.WebRTCUDPPort}},
	}
	if opts.PublishTCPPorts {
		// Raw desktop, file, audio and input services have no platform login.
		// Host-manager development access must never expose them to the LAN.
		for _, port := range []string{opts.NoVNCPort, opts.AudioServicePort, opts.WebRTCPort, opts.InputServicePort, opts.FileServicePort} {
			portBindings[nat.Port(port+"/tcp")] = []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "0"}}
		}
	}

	env := []string{
		fmt.Sprintf("RB_SESSION_ID=%s", opts.SessionID),
		fmt.Sprintf("RB_PROFILE_DIR=%s", opts.ProfileDir),
		fmt.Sprintf("RB_DOWNLOADS_DIR=%s", opts.DownloadsDir),
		fmt.Sprintf("RB_NOVNC_PORT=%s", opts.NoVNCPort),
		fmt.Sprintf("AUDIO_SERVICE_PORT=%s", opts.AudioServicePort),
		fmt.Sprintf("RB_WEBRTC_PORT=%s", opts.WebRTCPort),
		fmt.Sprintf("RB_INPUT_PORT=%s", opts.InputServicePort),
		fmt.Sprintf("RB_WEBRTC_UDP_PORT=%s", opts.WebRTCUDPPort),
		fmt.Sprintf("RB_WEBRTC_ICE_IP=%s", opts.WebRTCICEIP),
		fmt.Sprintf("RB_SCREEN_WIDTH=%s", opts.ScreenWidth),
		fmt.Sprintf("RB_SCREEN_HEIGHT=%s", opts.ScreenHeight),
		fmt.Sprintf("RB_SCREEN_DEPTH=%s", opts.ScreenDepth),
		fmt.Sprintf("RB_CHROME_WINDOW_TOP=%s", opts.ChromeWindowTop),
		fmt.Sprintf("RB_CHROME_WINDOW_BOTTOM=%s", opts.ChromeWindowBottom),
		fmt.Sprintf("RB_WEBRTC_WIDTH=%s", opts.WebRTCWidth),
		fmt.Sprintf("RB_WEBRTC_HEIGHT=%s", opts.WebRTCHeight),
		fmt.Sprintf("RB_WEBRTC_FRAMERATE=%s", opts.WebRTCFramerate),
		fmt.Sprintf("RB_WEBRTC_VIDEO_CODEC=%s", opts.WebRTCVideoCodec),
		fmt.Sprintf("RB_WEBRTC_VIDEO_BITRATE=%s", opts.WebRTCVideoBitrate),
		fmt.Sprintf("RB_WEBRTC_AUDIO_BITRATE=%s", opts.WebRTCAudioBitrate),
		fmt.Sprintf("RB_FILE_SERVICE_PORT=%s", opts.FileServicePort),
	}

	containerConfig := &container.Config{
		Image:        opts.Image,
		Env:          env,
		ExposedPorts: exposedPorts,
		Hostname:     opts.ContainerName,
		Labels: map[string]string{
			"remotebrowser.session.id":    opts.SessionID,
			"remotebrowser.managed":       "true",
			"remotebrowser.user.id":       fmt.Sprint(opts.UserID),
			"remotebrowser.deployment.id": opts.DeploymentID,
		},
	}

	hostConfig := &container.HostConfig{
		PortBindings:  portBindings,
		AutoRemove:    false,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources: container.Resources{
			NanoCPUs: opts.NanoCPUs,
			Memory:   opts.Memory,
		},
		ShmSize: opts.ShmSize,
		CapDrop: []string{"ALL"},
		Binds: []string{
			fmt.Sprintf("%s:%s", opts.HostProfileDir, opts.ProfileDir),
			fmt.Sprintf("%s:%s", opts.HostDownloadsDir, opts.DownloadsDir),
		},
	}

	if opts.NetworkName != "" {
		hostConfig.NetworkMode = container.NetworkMode(opts.NetworkName)
	}

	resp, err := c.cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, opts.ContainerName)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}

	if err := c.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Keep the named container associated with its database record. A failed
		// start is recoverable and must not hide a failed cleanup.
		return resp.ID, fmt.Errorf("start container: %w", err)
	}

	return resp.ID, nil
}

func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	return c.cli.ContainerStart(ctx, containerID, container.StartOptions{})
}

func (c *Client) StopContainer(ctx context.Context, containerID string, timeout int) error {
	return c.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

func (c *Client) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	return c.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: force})
}

func (c *Client) SetPersistentRestartPolicy(ctx context.Context, containerID string) error {
	_, err := c.cli.ContainerUpdate(ctx, containerID, container.UpdateConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
	})
	return err
}

func (c *Client) InspectContainer(ctx context.Context, containerID string) (*ContainerInfo, error) {
	info, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}
	ci := &ContainerInfo{
		ID:            info.ID,
		Name:          info.Name,
		State:         info.State.Status,
		Running:       info.State.Running,
		IP:            info.NetworkSettings.IPAddress,
		HostPorts:     make(map[string]string),
		Labels:        info.Config.Labels,
		Mounts:        make(map[string]string),
		RestartPolicy: string(info.HostConfig.RestartPolicy.Name),
	}
	if info.State.Health != nil {
		ci.Health = info.State.Health.Status
	}
	for _, mount := range info.Mounts {
		ci.Mounts[mount.Destination] = mount.Source
	}
	if ci.IP == "" {
		for _, network := range info.NetworkSettings.Networks {
			if network.IPAddress != "" {
				ci.IP = network.IPAddress
				break
			}
		}
	}
	// Extract mapped host ports for Mac Docker Desktop compatibility
	for port, bindings := range info.NetworkSettings.Ports {
		for _, b := range bindings {
			if b.HostPort != "" {
				ci.HostPorts[string(port)] = b.HostPort
				break
			}
		}
	}
	return ci, nil
}

func (c *Client) ListManagedContainers(ctx context.Context) ([]string, error) {
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, ct := range containers {
		managed := ct.Labels["remotebrowser.managed"] == "true"
		legacyName := false
		for _, name := range ct.Names {
			if strings.HasPrefix(strings.TrimPrefix(name, "/"), "rb-sess-") {
				legacyName = true
				break
			}
		}
		if managed || legacyName {
			ids = append(ids, ct.ID)
		}
	}
	return ids, nil
}

func (c *Client) PullImageIfNeeded(ctx context.Context, imageName string) error {
	_, _, err := c.cli.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		return nil // image exists
	}
	reader, err := c.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", imageName, err)
	}
	defer reader.Close()
	_, _ = io.Copy(io.Discard, reader)
	return nil
}

func (c *Client) EnsureNetwork(ctx context.Context, name string) (string, error) {
	c.networkMu.Lock()
	defer c.networkMu.Unlock()
	return c.ensureNetwork(ctx, name, nil)
}

type SessionNetworkOptions struct {
	Name, SessionID, DeploymentID string
	UserID                        int64
}

func (o SessionNetworkOptions) labels() map[string]string {
	return map[string]string{"remotebrowser.managed": "true", "remotebrowser.network.role": "instance",
		"remotebrowser.session.id": o.SessionID, "remotebrowser.deployment.id": o.DeploymentID,
		"remotebrowser.user.id": fmt.Sprint(o.UserID)}
}

func (c *Client) EnsureSessionNetwork(ctx context.Context, o SessionNetworkOptions) (string, error) {
	c.networkMu.Lock()
	defer c.networkMu.Unlock()
	return c.ensureNetwork(ctx, o.Name, o.labels())
}

func (c *Client) ensureNetwork(ctx context.Context, name string, labels map[string]string) (string, error) {
	info, err := c.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if IsNotFound(err) {
		if labels == nil {
			labels = map[string]string{"remotebrowser.managed": "true"}
		}
		resp, createErr := c.cli.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge", Labels: labels})
		if createErr != nil {
			return "", fmt.Errorf("create network %s: %w", name, createErr)
		}
		info.ID = resp.ID
		info.Name = name
		info.Labels = labels
	} else if err != nil {
		return "", err
	}
	for key, want := range labels {
		if info.Labels[key] != want {
			return "", fmt.Errorf("network ownership mismatch for %s", name)
		}
	}
	if err := c.connectManager(ctx, info.ID, name); err != nil {
		return "", err
	}
	return info.ID, nil
}

// connectManager makes the manager reachable from a per-instance network when
// it itself runs in Docker. A source checkout runs outside Docker and has no
// container endpoint, so it intentionally does not need this attachment.
func (c *Client) connectManager(ctx context.Context, networkID, networkName string) error {
	if c.managerContainerID == "" {
		return nil
	}
	info, err := c.cli.ContainerInspect(ctx, c.managerContainerID)
	if err != nil {
		return fmt.Errorf("inspect manager network membership: %w", err)
	}
	if info.NetworkSettings != nil {
		if endpoint, ok := info.NetworkSettings.Networks[networkName]; ok && endpoint.NetworkID == networkID {
			return nil
		}
	}
	if err := c.cli.NetworkConnect(ctx, networkID, info.ID, nil); err != nil {
		return fmt.Errorf("connect manager to session network %s: %w", networkName, err)
	}
	return nil
}

// MigrateSessionNetwork moves a verified instance off shared bridges and onto
// its own network. EnsureSessionNetwork also reconnects a replacement manager.
func (c *Client) MigrateSessionNetwork(ctx context.Context, containerID string, o SessionNetworkOptions) error {
	if _, err := c.EnsureSessionNetwork(ctx, o); err != nil {
		return err
	}
	info, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return err
	}
	if _, ok := info.NetworkSettings.Networks[o.Name]; !ok {
		if err := c.cli.NetworkConnect(ctx, o.Name, containerID, nil); err != nil {
			return fmt.Errorf("connect session to network %s: %w", o.Name, err)
		}
	}
	// A second bridge would be a bypass around instance isolation. The caller
	// has already verified this container's labels and persistent mounts.
	for name := range info.NetworkSettings.Networks {
		if name == o.Name {
			continue
		}
		if err := c.cli.NetworkDisconnect(ctx, name, containerID, true); err != nil {
			return fmt.Errorf("disconnect shared network %s: %w", name, err)
		}
	}
	return nil
}

func (c *Client) RemoveSessionNetwork(ctx context.Context, o SessionNetworkOptions) error {
	c.networkMu.Lock()
	defer c.networkMu.Unlock()
	info, err := c.cli.NetworkInspect(ctx, o.Name, network.InspectOptions{})
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for key, want := range o.labels() {
		if info.Labels[key] != want {
			return fmt.Errorf("refuse removing network with unverified ownership: %s", o.Name)
		}
	}
	managerID := ""
	if c.managerContainerID != "" {
		manager, err := c.cli.ContainerInspect(ctx, c.managerContainerID)
		if err != nil {
			return err
		}
		managerID = manager.ID
	}
	for id := range info.Containers {
		if id != managerID {
			return fmt.Errorf("instance network still has an unexpected endpoint: %s", id)
		}
	}
	if _, attached := info.Containers[managerID]; attached {
		if err := c.cli.NetworkDisconnect(ctx, info.ID, managerID, true); err != nil {
			return fmt.Errorf("detach manager before network deletion: %w", err)
		}
	}
	if err := c.cli.NetworkRemove(ctx, info.ID); err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}

type CreateSessionOptions struct {
	SessionID          string
	UserID             int64
	DeploymentID       string
	ContainerName      string
	Image              string
	ProfileDir         string
	DownloadsDir       string
	HostProfileDir     string
	HostDownloadsDir   string
	NoVNCPort          string
	AudioServicePort   string
	WebRTCPort         string
	InputServicePort   string
	WebRTCUDPPort      string
	WebRTCICEIP        string
	ScreenWidth        string
	ScreenHeight       string
	ScreenDepth        string
	ChromeWindowTop    string
	ChromeWindowBottom string
	WebRTCWidth        string
	WebRTCHeight       string
	WebRTCFramerate    string
	WebRTCVideoCodec   string
	WebRTCVideoBitrate string
	WebRTCAudioBitrate string
	FileServicePort    string
	NanoCPUs           int64
	Memory             int64
	ShmSize            int64
	NetworkName        string
	PublishTCPPorts    bool
}

type ContainerInfo struct {
	ID            string
	Name          string
	State         string
	Running       bool
	IP            string
	HostPorts     map[string]string // e.g. "6080/tcp" -> "55001"
	Health        string            // Docker health status, when a healthcheck is configured
	Labels        map[string]string
	Mounts        map[string]string // container destination -> verified host source
	RestartPolicy string
}

// IsNotFound distinguishes confirmed absence from an unavailable Docker daemon.
func IsNotFound(err error) bool { return client.IsErrNotFound(err) }
