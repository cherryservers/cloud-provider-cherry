package e2etest

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cherryservers/cherrygo/v3"
	"k8s.io/client-go/kubernetes"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/watch"
)

const (
	joinTimeout             = 210 * time.Second
	provisionTimeout        = 513 * time.Second
	controlPlaneNodeLabel   = "node-role.kubernetes.io/control-plane"
	userDataPath            = "./testdata/init-microk8s.yaml"
	userDataPathWithMetalLB = "./testdata/init-microk8s-with-metallb.yaml"
	serverImage             = "ubuntu_24_04_64bit"
	serverPlan              = "B1-4-4gb-80s-shared"
	region                  = "LT-Siauliai"
)

type node struct {
	server         cherrygo.Server
	cmdRunner      sshCmdRunner
	kubeconfigPath string
}

// runCmd runs a shell command on the node via SSH.
func (n node) runCmd(cmd string) (resp string, err error) {
	ip, err := serverPublicIP(n.server)
	if err != nil {
		return "", err
	}
	return n.cmdRunner.run(ip, cmd)
}

// join joins newNode to the base node's cluster.
// Blocks until the node is ready.
func (n *node) join(ctx context.Context, nn node, k8sclient kubernetes.Interface) error {
	ctx, cancel := context.WithTimeoutCause(ctx, joinTimeout, errors.New("node join timeout"))
	defer cancel()

	r, err := n.runCmd("microk8s add-node")
	if err != nil {
		return fmt.Errorf("couldn't get join URL from control plane node: %w", err)
	}
	ip, err := serverPublicIP(n.server)
	if err != nil {
		return err
	}

	// parse the microk8s join invitation response message
	// looking for public ip
	joinCmd := ""
	for line := range strings.Lines(r) {
		if strings.Contains(line, ip) {
			joinCmd = line[:len(line)-1] // strip newline
		}
	}

	_, err = nn.runCmd(joinCmd)
	if err != nil {
		return fmt.Errorf("couldn't execute join cmd: %w", err)
	}

	nn.addCpLabel(ctx)
	return untilNodeReady(ctx, nn, k8sclient)
}

// joinMany wraps join to join multiply nodes to the base node
// in a concurrent manner.
func (n *node) joinMany(ctx context.Context, nodes []node, k8sclient kubernetes.Interface) []error {
	errs := make([]error, len(nodes))
	c := make(chan error, len(nodes))

	for i := range len(nodes) {
		go func() {
			c <- n.join(ctx, nodes[i], k8sclient)
		}()
	}

	for i := range len(nodes) {
		errs[i] = <-c
	}
	return errs
}

// remove removes the provided node from the base node.
func (n *node) remove(ctx context.Context, nn *node) error {
	resp, err := n.runCmd("microk8s remove-node " + nn.server.Hostname + " --force")
	if err != nil {
		return fmt.Errorf("failed to remove node: %v: %s", err, resp)
	}
	return nil
}

// kubeconfig generates a kubeconfig file from the node
// and returns a path to it.
func (n *node) kubeconfig() (path string, cleanup func(), err error) {
	if n.kubeconfigPath != "" {
		return n.kubeconfigPath, func() {}, nil
	}
	const cmd = "microk8s config"

	k, err := n.runCmd(cmd)
	if err != nil {
		return "", nil, fmt.Errorf("failed to run '%s': %w", cmd, err)
	}
	f, err := os.CreateTemp("", "kubeconfig-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp file for kubeconfig: %w", err)
	}
	path = f.Name()
	cleanup = fileCleanup(path)

	_, err = f.WriteString(k)
	if err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("failed to write kubeconfig contents: %w", err)
	}

	err = f.Close()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to close kubeconfig file: %w", err)
	}

	n.kubeconfigPath = path
	return path, cleanup, nil
}

// addCpLabel adds the well-known control plane label
// to the node, since microk8s doesn't use it,
// but we need it for fip reconciliation.
func (n *node) addCpLabel(ctx context.Context) error {
	ctx, cancel := context.WithTimeoutCause(ctx, 64*time.Second, fmt.Errorf("timed out on label apply for %s", n.server.Hostname))
	defer cancel()

	return expBackoffWithContext(func() (bool, error) {
		_, err := n.runCmd("microk8s kubectl label nodes " + n.server.Hostname +
			" " + controlPlaneNodeLabel + "=\"\"")
		if err != nil {
			return false, nil
		}
		return true, nil
	}, defaultExpBackoffConfigWithContext(ctx))
}

// untilNodeReady watches the node until an event with ready status.
func untilNodeReady(ctx context.Context, n node, k8sclient kubernetes.Interface) error {
	lw := cache.NewListWatchFromClient(k8sclient.CoreV1().RESTClient(), "nodes", metav1.NamespaceAll, fields.Everything())

	_, err := watch.UntilWithSync(ctx, lw, &corev1.Node{}, nil, func(event apiwatch.Event) (done bool, err error) {
		node, ok := event.Object.(*corev1.Node)
		if !ok {
			return false, fmt.Errorf("unexpected object type: %T", event.Object)
		}
		if node.ObjectMeta.Name != n.server.Hostname {
			return false, nil
		}
		for _, c := range node.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("failed to reach joined node state: %w", err)
	}

	return nil
}

type nodeProvisioner interface {
	Provision(ctx context.Context) (*node, error)
}

type manyNodeProvisioner interface {
	nodeProvisioner
	ProvisionMany(ctx context.Context, n int) ([]*node, []error)
}

type microk8sNodeProvisioner struct {
	cherryClient cherrygo.Client
	projectID    int
	sshKeyID     string
	cmdRunner    sshCmdRunner
}

// Provision creates a Cherry Servers server and waits for k8s to be running.
func (np microk8sNodeProvisioner) Provision(ctx context.Context) (*node, error) {
	return np.provision(ctx, userDataPath)
}

func (np microk8sNodeProvisioner) provision(ctx context.Context, userDataPath string) (*node, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, provisionTimeout, errors.New("node provision timeout"))
	defer cancel()

	userDataRaw, err := os.ReadFile(userDataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read user data file: %w", err)
	}

	srv, _, err := np.cherryClient.Servers.Create(&cherrygo.CreateServer{
		ProjectID: np.projectID,
		Plan:      serverPlan,
		Region:    region,
		Image:     serverImage,
		UserData:  base64.StdEncoding.EncodeToString(userDataRaw),
		SSHKeys:   []string{np.sshKeyID},
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create server: %w", err)
	}

	expBackoffWithContext(func() (bool, error) {
		srv, _, err = np.cherryClient.Servers.Get(srv.ID, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get server: %w", err)
		}
		if srv.State == "active" {
			return true, nil
		}
		return false, nil
	}, defaultExpBackoffConfigWithContext(ctx))

	ip, err := serverPublicIP(srv)
	if err != nil {
		return nil, err
	}

	expBackoffWithContext(func() (bool, error) {
		// Check if kube-api is reachable. Non-zero exit code will be returned if not.
		_, err = np.cmdRunner.run(ip, "microk8s kubectl get nodes --no-headers")
		if err != nil {
			return false, nil
		}
		return true, nil
	}, defaultExpBackoffConfigWithContext(ctx))

	n := node{srv, np.cmdRunner, ""}
	n.addCpLabel(ctx)
	return &n, nil
}

// ProvisionMany wraps provision to create n Cherry Servers servers
// in a concurrent manner.
func (np microk8sNodeProvisioner) ProvisionMany(ctx context.Context, n int) ([]*node, []error) {
	type p struct {
		nn  *node
		err error
	}

	nodes := make([]*node, n)
	errs := make([]error, n)
	c := make(chan p, n)

	for range n {
		go func() {
			nn, err := np.Provision(ctx)
			c <- p{nn: nn, err: err}
		}()
	}
	for i := range n {
		provisioned := <-c
		nodes[i] = provisioned.nn
		errs[i] = provisioned.err

	}
	return nodes, errs
}

type microk8sMetalLBNodeProvisioner struct {
	microk8sNodeProvisioner
}

// Provision creates a Cherry Servers server and waits for k8s and metallb to be running.
func (np microk8sMetalLBNodeProvisioner) Provision(ctx context.Context) (*node, error) {
	return np.provision(ctx, userDataPathWithMetalLB)
}

func serverPublicIP(srv cherrygo.Server) (string, error) {
	for _, ip := range srv.IPAddresses {
		if ip.Type == "primary-ip" {
			return ip.Address, nil
		}
	}
	return "", fmt.Errorf("server %d has no public ip", srv.ID)
}

