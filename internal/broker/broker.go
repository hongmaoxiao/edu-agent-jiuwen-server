// Package broker starts and stops the per-account WorkSwarm containers. It is the only process with Docker access;
// the server reaches it over a Unix socket (cmd/broker). Callers choose nothing but the account and its assigned port:
// image, mounts, networks and limits come from the broker's own configuration. The layout follows the 智教云
// study broker (platform/sandbox/study-broker/broker.py in the client repository): one bridge network per account,
// the container port published on 127.0.0.1 only, and the course service joined to that network under the name course.
package broker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	owner         = "edu-agent-ws-v1"
	containerPort = "5173/tcp" // WorkSwarm's web server; it forwards /api and /ws to its own backend
)

type Config struct {
	Image           string       // the client image, as deploy/compose.yml builds it
	GatewayToken    string       // model gateway token written into each container (shared until per-account tokens)
	CourseContainer string       // joined to every account network under the alias course
	OriginHosts     string       // JIUWENSWARM_WS_ALLOWED_ORIGIN_HOSTS
	Subnets         netip.Prefix // carved into one /28 per port, never Docker's default pools, which 智教云 also draws from
	FirstPort       int
	LastPort        int
	Memory          int64 // bytes
	NanoCPUs        int64
	Pids            int64
	ReadyPath       string // polled on the published port until it answers 200
	ReadyTimeout    time.Duration
}

type Broker struct {
	config Config
	docker *dockerClient
	mu     sync.Mutex
	locks  map[string]*sync.Mutex
}

func New(config Config, dockerSocket string) (*Broker, error) {
	if config.Image == "" || config.CourseContainer == "" || len(config.GatewayToken) < 32 {
		return nil, errors.New("image, course container and a gateway token of at least 32 characters are required")
	}
	if !config.Subnets.Addr().Is4() || config.Subnets.Bits() > 28 || config.FirstPort < 1024 || config.LastPort > 65535 || config.FirstPort > config.LastPort {
		return nil, errors.New("invalid subnet or port range")
	}
	if config.LastPort-config.FirstPort+1 > 1<<(28-config.Subnets.Bits()) {
		return nil, errors.New("the subnet range has fewer /28 networks than the port range has ports")
	}
	return &Broker{config: config, docker: newDockerClient(dockerSocket), locks: map[string]*sync.Mutex{}}, nil
}

var accountPattern = regexp.MustCompile(`^acc_[0-9a-f]{32}$`)

// names derives every Docker name from the account id, so callers cannot point the broker at anything else.
type names struct{ key, container, network, volume string }

func (b *Broker) names(accountID string, port int) (names, error) {
	if !accountPattern.MatchString(accountID) || port < b.config.FirstPort || port > b.config.LastPort {
		return names{}, errInvalid
	}
	sum := sha256.Sum256([]byte(accountID))
	key := hex.EncodeToString(sum[:12])
	return names{key: key, container: "edu-ws-" + key, network: "edu-ws-" + key, volume: "edu-ws-" + key + "-home"}, nil
}

// subnet is the account's /28, fixed by its port so two accounts never share one.
func (b *Broker) subnet(port int) netip.Prefix {
	base := b.config.Subnets.Masked().Addr().As4()
	value := binary.BigEndian.Uint32(base[:]) + uint32(port-b.config.FirstPort)*16
	var addr [4]byte
	binary.BigEndian.PutUint32(addr[:], value)
	return netip.PrefixFrom(netip.AddrFrom4(addr), 28)
}

func (b *Broker) lock(key string) func() {
	b.mu.Lock()
	l, ok := b.locks[key]
	if !ok {
		l = &sync.Mutex{}
		b.locks[key] = l
	}
	b.mu.Unlock()
	l.Lock()
	return l.Unlock
}

var errInvalid = errors.New("invalid account or port")

func (b *Broker) labels(n names, port int) map[string]string {
	return map[string]string{"edu.owner": owner, "edu.identity": n.key, "edu.port": strconv.Itoa(port)}
}

// Ensure starts the account's container if needed and returns once WorkSwarm answers on its port.
func (b *Broker) Ensure(ctx context.Context, accountID string, port int) error {
	n, err := b.names(accountID, port)
	if err != nil {
		return err
	}
	defer b.lock(n.key)()
	if err := b.ensureNetwork(ctx, n, port); err != nil {
		return err
	}
	if err := b.ensureVolume(ctx, n, port); err != nil {
		return err
	}
	var info containerInfo
	found, err := b.docker.get(ctx, "/containers/"+n.container+"/json", &info)
	if err != nil {
		return err
	}
	if found && !owned(info.Config.Labels, n.key) {
		return fmt.Errorf("container %s is not ours", n.container)
	}
	if !found {
		if err := b.docker.call(ctx, "POST", "/containers/create?name="+n.container, b.spec(n, port), nil, 201); err != nil {
			return err
		}
	}
	if err := b.docker.call(ctx, "POST", "/containers/"+n.container+"/start", nil, nil, 204, 304); err != nil {
		return err
	}
	return b.waitReady(ctx, n, port)
}

func owned(labels map[string]string, key string) bool {
	return labels["edu.owner"] == owner && labels["edu.identity"] == key
}

func (b *Broker) ensureNetwork(ctx context.Context, n names, port int) error {
	var network networkInfo
	found, err := b.docker.get(ctx, "/networks/"+n.network, &network)
	if err != nil {
		return err
	}
	if found && !owned(network.Labels, n.key) {
		return fmt.Errorf("network %s is not ours", n.network)
	}
	if !found {
		create := map[string]any{"Name": n.network, "Driver": "bridge", "Labels": b.labels(n, port),
			"IPAM": map[string]any{"Config": []map[string]string{{"Subnet": b.subnet(port).String()}}}}
		if err := b.docker.call(ctx, "POST", "/networks/create", create, nil, 201); err != nil {
			return err
		}
	}
	if network.has(b.config.CourseContainer) {
		return nil
	}
	connect := map[string]any{"Container": b.config.CourseContainer, "EndpointConfig": map[string]any{"Aliases": []string{"course"}}}
	return b.docker.call(ctx, "POST", "/networks/"+n.network+"/connect", connect, nil, 200)
}

func (b *Broker) ensureVolume(ctx context.Context, n names, port int) error {
	var volume struct{ Labels map[string]string }
	found, err := b.docker.get(ctx, "/volumes/"+n.volume, &volume)
	if err != nil {
		return err
	}
	if found {
		if !owned(volume.Labels, n.key) {
			return fmt.Errorf("volume %s is not ours", n.volume)
		}
		return nil
	}
	return b.docker.call(ctx, "POST", "/volumes/create", map[string]any{"Name": n.volume, "Labels": b.labels(n, port)}, nil, 201)
}

// spec mirrors the client service in deploy/compose.yml of the client repository: no secrets but the gateway token,
// read-only root, no capabilities, memory and process limits.
func (b *Broker) spec(n names, port int) map[string]any {
	env := []string{
		"HOME=/home/app", "JIUWENSWARM_HOME=/home/app", "JIUWENSWARM_CONFIG_URL=off", "JIUWENSWARM_ENABLE_ORIGIN_CHECK=1",
		"JIUWENSWARM_WS_ALLOWED_ORIGIN_HOSTS=" + b.config.OriginHosts, "FRONTEND_HOST=0.0.0.0",
		"EDU_PLATFORM_URL=http://course:8000", "EDU_LABS_DIR=/home/app/labs",
		"AGENT_GATEWAY_URL=http://course:8000/api/agent/gateway/v1", "AGENT_GATEWAY_TOKEN=" + b.config.GatewayToken,
	}
	return map[string]any{
		"Image": b.config.Image, "Env": env, "Labels": b.labels(n, port),
		"ExposedPorts": map[string]any{containerPort: map[string]any{}},
		"HostConfig": map[string]any{
			"Binds":          []string{n.volume + ":/home/app"},
			"PortBindings":   map[string]any{containerPort: []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(port)}}},
			"NetworkMode":    n.network,
			"ReadonlyRootfs": true,
			"Tmpfs":          map[string]string{"/tmp": "size=512m,mode=1777"},
			"CapDrop":        []string{"ALL"},
			"SecurityOpt":    []string{"no-new-privileges:true"},
			"Init":           true,
			"Memory":         b.config.Memory,
			"NanoCpus":       b.config.NanoCPUs,
			"PidsLimit":      b.config.Pids,
			"LogConfig":      map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "5m", "max-file": "2"}},
		},
	}
}

func (b *Broker) waitReady(ctx context.Context, n names, port int) error {
	ctx, cancel := context.WithTimeout(ctx, b.config.ReadyTimeout)
	defer cancel()
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + b.config.ReadyPath
	for {
		var info containerInfo
		if _, err := b.docker.get(ctx, "/containers/"+n.container+"/json", &info); err != nil {
			return err
		}
		if !info.State.Running {
			return fmt.Errorf("container %s exited", n.container)
		}
		if binding := info.NetworkSettings.Ports[containerPort]; len(binding) != 1 || binding[0].HostIP != "127.0.0.1" || binding[0].HostPort != strconv.Itoa(port) {
			return fmt.Errorf("container %s is not bound to 127.0.0.1:%d", n.container, port)
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("container %s not ready: %w", n.container, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// Stop removes the container and its network; the volume with the account's files stays for next time.
// A fresh container on every start also picks up a new image after a deploy.
func (b *Broker) Stop(ctx context.Context, accountID string, port int) error {
	n, err := b.names(accountID, port)
	if err != nil {
		return err
	}
	defer b.lock(n.key)()
	var info containerInfo
	found, err := b.docker.get(ctx, "/containers/"+n.container+"/json", &info)
	if err != nil {
		return err
	}
	if found {
		if !owned(info.Config.Labels, n.key) {
			return fmt.Errorf("container %s is not ours", n.container)
		}
		if err := b.docker.call(ctx, "DELETE", "/containers/"+n.container+"?force=1", nil, nil, 204, 404); err != nil {
			return err
		}
	}
	var network networkInfo
	found, err = b.docker.get(ctx, "/networks/"+n.network, &network)
	if err != nil || !found {
		return err
	}
	if !owned(network.Labels, n.key) {
		return fmt.Errorf("network %s is not ours", n.network)
	}
	if network.has(b.config.CourseContainer) {
		disconnect := map[string]any{"Container": b.config.CourseContainer, "Force": true}
		if err := b.docker.call(ctx, "POST", "/networks/"+n.network+"/disconnect", disconnect, nil, 200); err != nil {
			return err
		}
	}
	return b.docker.call(ctx, "DELETE", "/networks/"+n.network, nil, nil, 204, 404)
}

// Reconnect joins the course service back to every account network that lost it. Recreating the course
// container (a redeploy) drops its extra networks, and running workspaces would lose the model gateway.
func (b *Broker) Reconnect(ctx context.Context) error {
	filter := url.QueryEscape(`{"label":["edu.owner=` + owner + `"]}`)
	var networks []struct {
		Name   string
		Labels map[string]string
	}
	if err := b.docker.call(ctx, "GET", "/networks?filters="+filter, nil, &networks, 200); err != nil {
		return err
	}
	for _, listed := range networks {
		var network networkInfo
		if found, err := b.docker.get(ctx, "/networks/"+listed.Name, &network); err != nil || !found || network.has(b.config.CourseContainer) {
			if err != nil {
				return err
			}
			continue
		}
		connect := map[string]any{"Container": b.config.CourseContainer, "EndpointConfig": map[string]any{"Aliases": []string{"course"}}}
		if err := b.docker.call(ctx, "POST", "/networks/"+listed.Name+"/connect", connect, nil, 200); err != nil {
			return err
		}
	}
	return nil
}

type containerInfo struct {
	Config          struct{ Labels map[string]string }
	State           struct{ Running bool }
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
	}
}

type networkInfo struct {
	Labels     map[string]string
	Containers map[string]struct{ Name string }
}

func (n networkInfo) has(container string) bool {
	for _, c := range n.Containers {
		if c.Name == container {
			return true
		}
	}
	return false
}
