package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

func dockerHTTP(t *testing.T, managerID string, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.48"), client.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return &Client{cli: cli, managerContainerID: managerID}
}
func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func apiPath(r *http.Request) string { return strings.TrimPrefix(r.URL.Path, "/v1.48") }
func networkOpts() SessionNetworkOptions {
	return SessionNetworkOptions{Name: "rb-net-deploy-session", DeploymentID: "deploy", SessionID: "sess_example", UserID: 12}
}
func networkReply(o SessionNetworkOptions, endpoints map[string]any) map[string]any {
	return map[string]any{"Id": "network-id", "Name": o.Name, "Driver": "bridge", "Labels": o.labels(), "Containers": endpoints}
}
func managerReply(networks map[string]any) map[string]any {
	return map[string]any{"Id": "manager-current", "NetworkSettings": map[string]any{"Networks": networks}}
}

func TestCreateContainerPublishesOnlyAuthenticatedTransportExternally(t *testing.T) {
	var captured struct {
		container.Config
		HostConfig container.HostConfig
	}
	c := dockerHTTP(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch apiPath(r) {
		case "/containers/create":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Error(err)
			}
			jsonReply(w, map[string]any{"Id": "new-container"})
		case "/containers/new-container/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	})
	_, err := c.CreateSessionContainer(context.Background(), CreateSessionOptions{SessionID: "sess_example", UserID: 12, DeploymentID: "deploy", ContainerName: "instance", Image: "test", ProfileDir: "/profile", DownloadsDir: "/downloads", HostProfileDir: "/data/p", HostDownloadsDir: "/data/d", NoVNCPort: "6080", AudioServicePort: "6081", WebRTCPort: "6082", InputServicePort: "6084", FileServicePort: "8081", WebRTCUDPPort: "45000", NetworkName: "private-instance", PublishTCPPorts: true})
	if err != nil {
		t.Fatal(err)
	}
	for port, bindings := range captured.HostConfig.PortBindings {
		for _, binding := range bindings {
			if strings.HasSuffix(string(port), "/tcp") && binding.HostIP != "127.0.0.1" {
				t.Errorf("unauthenticated TCP exposed: %s %+v", port, binding)
			}
		}
	}
	if string(captured.HostConfig.NetworkMode) != "private-instance" {
		t.Fatal("container joined shared network")
	}
	if captured.HostConfig.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatal("restart policy missing")
	}
	if captured.Labels["remotebrowser.deployment.id"] != "deploy" || captured.Labels["remotebrowser.user.id"] != "12" {
		t.Fatal("ownership labels missing")
	}
}

func TestEnsureNetworkAlreadyConnectedMakesNoMutations(t *testing.T) {
	o := networkOpts()
	c := dockerHTTP(t, "manager-current", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unnecessary mutation: %s", r.URL.Path)
		}
		switch apiPath(r) {
		case "/networks/" + o.Name:
			jsonReply(w, networkReply(o, nil))
		case "/containers/manager-current/json":
			jsonReply(w, managerReply(map[string]any{o.Name: map[string]any{"NetworkID": "network-id"}}))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	for i := 0; i < 3; i++ {
		if _, err := c.EnsureSessionNetwork(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagerReplacementReconnectsToExistingInstanceNetwork(t *testing.T) {
	o := networkOpts()
	connections := 0
	c := dockerHTTP(t, "new-manager-hostname", func(w http.ResponseWriter, r *http.Request) {
		switch apiPath(r) {
		case "/networks/" + o.Name:
			jsonReply(w, networkReply(o, nil))
		case "/containers/new-manager-hostname/json":
			jsonReply(w, managerReply(nil))
		case "/networks/network-id/connect":
			var request struct{ Container string }
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.Container != "manager-current" {
				t.Errorf("old manager identity: %s", request.Container)
			}
			connections++
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	if _, err := c.EnsureSessionNetwork(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if connections != 1 {
		t.Fatalf("manager reconnect count: %d", connections)
	}
}

func TestEnsureNewNetworkHasOwnershipAndInternetEgress(t *testing.T) {
	o := networkOpts()
	var created struct {
		Name     string
		Driver   string
		Internal bool
		Labels   map[string]string
	}
	c := dockerHTTP(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch apiPath(r) {
		case "/networks/" + o.Name:
			w.WriteHeader(404)
			jsonReply(w, map[string]string{"message": "not found"})
		case "/networks/create":
			_ = json.NewDecoder(r.Body).Decode(&created)
			jsonReply(w, map[string]string{"Id": "network-id"})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	if _, err := c.EnsureSessionNetwork(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if created.Internal || created.Driver != "bridge" {
		t.Fatal("instance network cannot reach internet")
	}
	for key, want := range o.labels() {
		if created.Labels[key] != want {
			t.Errorf("label %s got %s", key, created.Labels[key])
		}
	}
}

func TestNetworkOwnershipMismatchIsNotAdopted(t *testing.T) {
	o := networkOpts()
	c := dockerHTTP(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("foreign network changed: %s", r.URL.Path)
		}
		foreign := networkReply(o, nil)
		foreign["Labels"] = map[string]string{"remotebrowser.deployment.id": "different-deployment"}
		jsonReply(w, foreign)
	})
	if _, err := c.EnsureSessionNetwork(context.Background(), o); err == nil {
		t.Fatal("foreign network adopted")
	}
	if err := c.RemoveSessionNetwork(context.Background(), o); err == nil {
		t.Fatal("foreign network deleted")
	}
}

func TestNetworkDeletionDetachesOnlyManagerThenRemoves(t *testing.T) {
	o := networkOpts()
	var operations []string
	c := dockerHTTP(t, "manager-current", func(w http.ResponseWriter, r *http.Request) {
		switch apiPath(r) {
		case "/networks/" + o.Name:
			jsonReply(w, networkReply(o, map[string]any{"manager-current": map[string]any{"Name": "manager"}}))
		case "/containers/manager-current/json":
			jsonReply(w, managerReply(nil))
		case "/networks/network-id/disconnect":
			var request struct{ Container string }
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.Container != "manager-current" {
				t.Error("disconnected wrong endpoint")
			}
			operations = append(operations, "disconnect")
			w.WriteHeader(200)
		case "/networks/network-id":
			if r.Method != http.MethodDelete {
				t.Errorf("unexpected method: %s", r.Method)
			}
			operations = append(operations, "remove")
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	if err := c.RemoveSessionNetwork(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(operations) != "[disconnect remove]" {
		t.Fatalf("cleanup order: %v", operations)
	}
}

func TestNetworkWithForeignEndpointIsNotDisconnectedOrDeleted(t *testing.T) {
	o := networkOpts()
	c := dockerHTTP(t, "manager-current", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("foreign endpoint changed: %s", r.URL.Path)
		}
		switch apiPath(r) {
		case "/networks/" + o.Name:
			jsonReply(w, networkReply(o, map[string]any{"different-instance": map[string]any{}}))
		case "/containers/manager-current/json":
			jsonReply(w, managerReply(nil))
		default:
			w.WriteHeader(500)
		}
	})
	if err := c.RemoveSessionNetwork(context.Background(), o); err == nil {
		t.Fatal("network containing other instance deleted")
	}
}

func TestNetworkMigrationDropsEverySharedBridge(t *testing.T) {
	o := networkOpts()
	var mu sync.Mutex
	var connections, disconnections []string
	c := dockerHTTP(t, "", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch apiPath(r) {
		case "/networks/" + o.Name:
			jsonReply(w, networkReply(o, nil))
		case "/containers/session-container/json":
			jsonReply(w, map[string]any{"Id": "session-container", "NetworkSettings": map[string]any{"Networks": map[string]any{"legacy-shared": map[string]any{}, "another-shared": map[string]any{}}}})
		case "/networks/" + o.Name + "/connect":
			connections = append(connections, o.Name)
			w.WriteHeader(200)
		case "/networks/legacy-shared/disconnect":
			disconnections = append(disconnections, "legacy-shared")
			w.WriteHeader(200)
		case "/networks/another-shared/disconnect":
			disconnections = append(disconnections, "another-shared")
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	if err := c.MigrateSessionNetwork(context.Background(), "session-container", o); err != nil {
		t.Fatal(err)
	}
	if len(connections) != 1 || len(disconnections) != 2 {
		t.Fatalf("isolation incomplete: connect=%v disconnect=%v", connections, disconnections)
	}
}
