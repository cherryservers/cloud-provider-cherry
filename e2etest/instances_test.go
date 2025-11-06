package e2etest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/watch"
)

func untilNodeGone(ctx context.Context, n corev1.Node, k8sClient kubernetes.Interface) error {
	const nodeDeletionTimeout = 120 * time.Second

	ctx, cancel := context.WithTimeoutCause(ctx, nodeDeletionTimeout, errors.New("no node deletion event before timeout"))
	defer cancel()

	lw := cache.NewListWatchFromClient(k8sClient.CoreV1().RESTClient(), "nodes", metav1.NamespaceAll, fields.Everything())

	_, err := watch.UntilWithSync(ctx, lw, &corev1.Node{}, nil, func(event apiwatch.Event) (done bool, err error) {
		node, ok := event.Object.(*corev1.Node)
		if !ok {
			return false, fmt.Errorf("unexpected object type: %T", event.Object)
		}

		if node.ObjectMeta.Name == n.ObjectMeta.Name && event.Type == apiwatch.Deleted {
			return true, nil
		}
		return false, nil
	})

	return err
}

// TestNodeAddDelete tests that the node controller handles added and removed nodes.
// Combining these tests allows us to re-use infrastructure,
// which reduces test run times.
func TestNodeAddDelete(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	cfg := testEnvConfig{name: "kubernetes-ccm-test-node-add-delete"}

	env := setupTestEnv(ctx, t, cfg)
	var np nodeProvisioner = env.nodeProvisioner

	n, err := np.Provision(ctx)
	if err != nil {
		t.Fatalf("failed to provision node: %v", err)
	}

	err = env.mainNode.Join(ctx, n)
	if err != nil {
		t.Fatalf("failed to join nodes: %v", err)
	}

	k8sn, err := env.k8sClient.CoreV1().Nodes().Get(ctx, n.Server.Hostname, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get node :%v", err)
	}

	// check provider id
	wantProviderID := "cherryservers://" + strconv.Itoa(n.Server.ID)
	if got, want := k8sn.Spec.ProviderID, wantProviderID; got != want {
		t.Errorf("got provider ID %q, want %q", got, want)
	}

	// check metadata
	if got, want := k8sn.Labels["topology.kubernetes.io/region"], n.Server.Region.Slug; got != want {
		t.Errorf("got region %q, want %q", got, want)
	}

	wantPlan := n.Server.Plan.Slug
	if got, want := k8sn.Labels["node.kubernetes.io/instance-type"], wantPlan; got != want {
		t.Errorf("got instance type %q, want %q", got, want)
	}

	for _, address := range k8sn.Status.Addresses {
		switch address.Type {
		case corev1.NodeHostName:
			if address.Address != n.Server.Hostname {
				t.Errorf("got hostname %q, want %q", address.Address, n.Server.Hostname)
			}
		case corev1.NodeExternalIP:
			found := false
			for _, srvAddress := range n.Server.IPAddresses {
				if srvAddress.Address == address.Address && srvAddress.Type == "primary-ip" {
					found = true
				}
			}
			if !found {
				t.Errorf("no matching public server ip for node external ip %q", address.Address)
			}
		case corev1.NodeInternalIP:
			found := false
			for _, srvAddress := range n.Server.IPAddresses {
				if srvAddress.Address == address.Address && srvAddress.Type == "private-ip" {
					found = true
				}
			}
			if !found {
				t.Errorf("no matching private server ip for node internal ip %q", address.Address)
			}
		}

	}

	// node deletion
	_, _, err = cherryClient.Servers.Delete(n.Server.ID)
	if err != nil {
		t.Fatalf("failed to delete server: %v", err)
	}
	err = untilNodeGone(ctx, *k8sn, env.k8sClient)
	if err != nil {
		t.Errorf("no deletion event for node %q, error: %v", k8sn.ObjectMeta.Name, err)
	}

}
