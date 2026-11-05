package broker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDocker keeps just enough Engine API state for Ensure and Stop. A started container "serves" WorkSwarm
// by pointing its published port at a local test server.
type fakeDocker struct {
	mu         sync.Mutex
	containers map[string]map[string]any // name → create body
	running    map[string]bool
	networks   map[string]map[string]any // name → create body plus "Containers"
	volumes    map[string]map[string]any
	calls      []string
}

func startFakeDocker(t *testing.T) (*fakeDocker, string) {
	t.Helper()
	f := &fakeDocker{containers: map[string]map[string]any{}, running: map[string]bool{}, networks: map[string]map[string]any{}, volumes: map[string]map[string]any{}}
	// macOS 的默认临时目录太长，放不下 unix socket 路径（上限约 104 字节）。
	dir, err := os.MkdirTemp("/tmp", "broker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(f.serve)}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return f, socket
}

func (f *fakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, apiVersion)
	f.calls = append(f.calls, r.Method+" "+path)
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	reply := func(status int, value any) {
		w.WriteHeader(status)
		if value != nil {
			json.NewEncoder(w).Encode(value)
		}
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == "GET" && path == "/networks":
		var list []map[string]any
		for name, n := range f.networks {
			list = append(list, map[string]any{"Name": name, "Labels": n["Labels"]})
		}
		reply(200, list)
	case r.Method == "GET" && parts[0] == "networks":
		if n, ok := f.networks[parts[1]]; ok {
			reply(200, n)
			return
		}
		reply(404, map[string]string{"message": "no such network"})
	case r.Method == "POST" && path == "/networks/create":
		body["Containers"] = map[string]any{}
		f.networks[body["Name"].(string)] = body
		reply(201, map[string]string{"Id": "n"})
	case r.Method == "POST" && len(parts) == 3 && parts[0] == "networks" && parts[2] == "connect":
		f.networks[parts[1]]["Containers"].(map[string]any)["id-"+body["Container"].(string)] = map[string]any{"Name": body["Container"]}
		reply(200, nil)
	case r.Method == "POST" && len(parts) == 3 && parts[0] == "networks" && parts[2] == "disconnect":
		delete(f.networks[parts[1]]["Containers"].(map[string]any), "id-"+body["Container"].(string))
		reply(200, nil)
	case r.Method == "DELETE" && parts[0] == "networks":
		delete(f.networks, parts[1])
		reply(204, nil)
	case r.Method == "GET" && parts[0] == "volumes":
		if v, ok := f.volumes[parts[1]]; ok {
			reply(200, v)
			return
		}
		reply(404, map[string]string{"message": "no such volume"})
	case r.Method == "POST" && path == "/volumes/create":
		f.volumes[body["Name"].(string)] = body
		reply(201, body)
	case r.Method == "POST" && path == "/containers/create":
		f.containers[r.URL.Query().Get("name")] = body
		reply(201, map[string]string{"Id": "c"})
	case r.Method == "POST" && len(parts) == 3 && parts[2] == "start":
		if f.running[parts[1]] {
			reply(304, nil)
			return
		}
		f.running[parts[1]] = true
		reply(204, nil)
	case r.Method == "GET" && len(parts) == 3 && parts[0] == "containers" && parts[2] == "json":
		spec, ok := f.containers[parts[1]]
		if !ok {
			reply(404, map[string]string{"message": "no such container"})
			return
		}
		host := spec["HostConfig"].(map[string]any)
		reply(200, map[string]any{"Config": map[string]any{"Labels": spec["Labels"]}, "State": map[string]any{"Running": f.running[parts[1]]},
			"NetworkSettings": map[string]any{"Ports": host["PortBindings"]}})
	case r.Method == "DELETE" && parts[0] == "containers":
		delete(f.containers, parts[1])
		delete(f.running, parts[1])
		reply(204, nil)
	default:
		reply(500, map[string]string{"message": "unexpected " + r.Method + " " + path})
	}
}

const account = "acc_0123456789abcdef0123456789abcdef"

// workswarm answers the readiness path on a free loopback port and returns that port.
func workswarm(t *testing.T) int {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/status" {
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	n, _ := strconv.Atoi(port)
	return n
}

func newBroker(t *testing.T, socket string, port int) *Broker {
	t.Helper()
	b, err := New(Config{
		Image: "edu-agent-jiuwen-client:test", GatewayKey: strings.Repeat("k", 40), CourseContainer: "edu-agent-jiuwen-course-1",
		OriginHosts: "agent.hrtiger.cn", Subnets: netip.MustParsePrefix("10.231.0.0/16"), FirstPort: port - 5, LastPort: port + 5,
		Memory: 4 << 30, NanoCPUs: 2e9, Pids: 512, ReadyTimeout: 5 * time.Second,
	}, socket)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEnsureBuildsAnIsolatedContainerAndStopKeepsTheFiles(t *testing.T) {
	docker, socket := startFakeDocker(t)
	port := workswarm(t)
	b := newBroker(t, socket, port)
	n, _ := b.names(account, port)

	if err := b.Ensure(context.Background(), account, port); err != nil {
		t.Fatal(err)
	}
	network := docker.networks[n.network]
	subnet := network["IPAM"].(map[string]any)["Config"].([]any)[0].(map[string]any)["Subnet"]
	if subnet != b.subnet(port).String() || !strings.HasPrefix(subnet.(string), "10.231.") {
		t.Errorf("account network must use its own /28 from 10.231.0.0/16, got %v", subnet)
	}
	if len(network["Containers"].(map[string]any)) != 1 {
		t.Errorf("the course service must join the account network: %v", network["Containers"])
	}
	spec := docker.containers[n.container]
	host := spec["HostConfig"].(map[string]any)
	binding := host["PortBindings"].(map[string]any)[containerPort].([]any)[0].(map[string]any)
	if binding["HostIp"] != "127.0.0.1" || binding["HostPort"] != strconv.Itoa(port) {
		t.Errorf("port must be published on loopback only: %v", binding)
	}
	if host["ReadonlyRootfs"] != true || host["CapDrop"].([]any)[0] != "ALL" || host["NetworkMode"] != n.network || host["Binds"].([]any)[0] != n.volume+":/home/app" {
		t.Errorf("container must match the client service's hardening: %v", host)
	}
	var token string
	for _, value := range spec["Env"].([]any) {
		if strings.Contains(value.(string), "DEEPSEEK") || strings.Contains(value.(string), b.config.GatewayKey) {
			t.Errorf("no provider key or signing key may reach the container: %v", value)
		}
		if rest, ok := strings.CutPrefix(value.(string), "AGENT_GATEWAY_TOKEN="); ok {
			token = rest
		}
	}
	// 签名用同一把钥匙核对，载荷里是这个账号：课程网关就按这个人计量。
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "edp" {
		t.Fatalf("container must hold a signed gateway token, got %q", token)
	}
	mac := hmac.New(sha256.New, []byte(b.config.GatewayKey))
	mac.Write([]byte("edp." + parts[1]))
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if parts[2] != base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) || !strings.Contains(string(claims), `"acc":"`+account+`"`) {
		t.Errorf("gateway token must be signed for this account, got claims %s", claims)
	}

	if err := b.Ensure(context.Background(), account, port); err != nil {
		t.Fatalf("ensuring a running container must succeed: %v", err)
	}
	// 课程服务重建后不在账号网络里了：Reconnect 把它接回来。
	network["Containers"] = map[string]any{}
	if err := b.Reconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(docker.networks[n.network]["Containers"].(map[string]any)) != 1 {
		t.Error("reconnect must join the course service back to the account network")
	}
	if err := b.Stop(context.Background(), account, port); err != nil {
		t.Fatal(err)
	}
	if _, ok := docker.containers[n.container]; ok {
		t.Error("stop must remove the container")
	}
	if _, ok := docker.networks[n.network]; ok {
		t.Error("stop must remove the account network")
	}
	if _, ok := docker.volumes[n.volume]; !ok {
		t.Error("stop must keep the account's files")
	}
	if err := b.Stop(context.Background(), account, port); err != nil {
		t.Errorf("stopping twice must be harmless: %v", err)
	}
}

func TestBrokerRefusesForeignObjectsAndBadInput(t *testing.T) {
	docker, socket := startFakeDocker(t)
	port := workswarm(t)
	b := newBroker(t, socket, port)
	for _, bad := range []struct {
		account string
		port    int
	}{{"acc_../../etc", port}, {"someone", port}, {account, port + 100}, {account, 80}} {
		if err := b.Ensure(context.Background(), bad.account, bad.port); err != errInvalid {
			t.Errorf("Ensure(%q, %d) must be rejected, got %v", bad.account, bad.port, err)
		}
	}
	n, _ := b.names(account, port)
	docker.networks[n.network] = map[string]any{"Labels": map[string]string{"com.example": "someone else"}, "Containers": map[string]any{}}
	if err := b.Ensure(context.Background(), account, port); err == nil || !strings.Contains(err.Error(), "not ours") {
		t.Errorf("a network the broker did not create must not be used: %v", err)
	}
	if err := b.Stop(context.Background(), account, port); err == nil {
		t.Error("a network the broker did not create must not be removed")
	}
}

func TestSubnetsNeverOverlap(t *testing.T) {
	b := &Broker{config: Config{Subnets: netip.MustParsePrefix("10.231.0.0/16"), FirstPort: 57000, LastPort: 57999}}
	seen := map[netip.Prefix]bool{}
	for port := 57000; port <= 57999; port++ {
		subnet := b.subnet(port)
		if seen[subnet] || !b.config.Subnets.Contains(subnet.Addr()) {
			t.Fatalf("port %d: subnet %v overlaps or leaves the range", port, subnet)
		}
		seen[subnet] = true
	}
	if _, err := New(Config{Image: "i", CourseContainer: "c", GatewayKey: strings.Repeat("k", 40), Subnets: netip.MustParsePrefix("10.231.0.0/24"), FirstPort: 57000, LastPort: 57999}, ""); err == nil {
		t.Error("a range too small for the ports must be rejected")
	}
}
