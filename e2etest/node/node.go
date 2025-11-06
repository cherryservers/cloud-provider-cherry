package node

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cherryservers/cherrygo/v3"
	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	"github.com/cherryservers/cloud-provider-cherry-tests/backoff"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	Region                = "LT-Siauliai"
	ControlPlaneNodeLabel = "node-role.kubernetes.io/control-plane"
	informerResyncPeriod  = 5 * time.Second
	informerTimeout       = 120 * time.Second
)

type Node struct {
	Server    cherrygo.Server
	K8sclient kubernetes.Interface
	cmdRunner sshCmdRunner
}

func (n Node) Deploy(manifest io.Reader) error {
	r, err := n.RunCmd("microk8s kubectl apply -f - ", manifest)
	if err != nil {
		return fmt.Errorf("failed to apply manifest: %s", r)
	}
	return nil
}

// RunCmd runs a shell command on the node via SSH.
// Passing nil stdin is fine.
func (n Node) RunCmd(cmd string, stdin io.Reader) (resp string, err error) {
	ip, err := serverPublicIP(n.Server)
	if err != nil {
		return "", err
	}
	return n.cmdRunner.run(ip, cmd, stdin)
}

// Join joins newNode to the base node's cluster.
// Blocks until the node is ready.
// The base node MUST be a control plane node.
// The base node's cluster MUST have the CCM running.
func (n *Node) Join(ctx context.Context, nn Node) error {
	const timeout = 210 * time.Second

	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errors.New("node join timeout"))
	defer cancel()

	r, err := n.RunCmd("microk8s add-node", nil)
	if err != nil {
		return fmt.Errorf("couldn't get join URL from control plane node: %w", err)
	}
	ip, err := serverPublicIP(n.Server)
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

	_, err = nn.RunCmd(joinCmd, nil)
	if err != nil {
		return fmt.Errorf("couldn't execute join cmd: %w", err)
	}

	nn.addCpLabel(ctx)
	kubeconfig, err := nn.RunCmd("microk8s config", nil)
	if err != nil {
		return fmt.Errorf("failed to get k8s config: %w", err)
	}

	nn.K8sclient, err = newK8sClient(kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to create k8s client: %w", err)
	}
	return nn.UntilHasProviderID(ctx)
}

// JoinBatch wraps join to join multiply nodes to the base node
// in a concurrent manner.
func (n *Node) JoinBatch(ctx context.Context, nodes []Node) []error {
	errs := make([]error, len(nodes))
	c := make(chan error, len(nodes))

	for i := range len(nodes) {
		go func() {
			c <- n.Join(ctx, nodes[i])
		}()
	}

	for i := range len(nodes) {
		errs[i] = <-c
	}
	return errs
}

// Remove removes the provided node from the base node.
func (n *Node) Remove(nn *Node) error {
	resp, err := n.RunCmd("microk8s remove-node "+nn.Server.Hostname+" --force", nil)
	if err != nil {
		return fmt.Errorf("failed to remove node: %v: %s", err, resp)
	}
	return nil
}

// addCpLabel adds the well-known control plane label
// to the node, since microk8s doesn't use it,
// but we need it for fip reconciliation.
func (n *Node) addCpLabel(ctx context.Context) error {
	ctx, cancel := context.WithTimeoutCause(ctx, 64*time.Second, fmt.Errorf("timed out on label apply for %s", n.Server.Hostname))
	defer cancel()

	return backoff.ExpBackoffWithContext(func() (bool, error) {
		_, err := n.RunCmd("microk8s kubectl label nodes "+n.Server.Hostname+
			" "+ControlPlaneNodeLabel+"=\"\"", nil)
		if err != nil {
			return false, nil
		}
		return true, nil
	}, backoff.DefaultExpBackoffConfigWithContext(ctx))
}

func (n *Node) UntilHasProviderID(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, informerTimeout)
	hasID := false

	factory := informers.NewSharedInformerFactory(n.K8sclient, informerResyncPeriod)
	_, err := factory.Core().V1().Nodes().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, newObj any) {
			newNode, _ := newObj.(*corev1.Node)
			if newNode.Name != n.Server.Hostname {
				return
			}
			if newNode.Spec.ProviderID != "" {
				hasID = true
				cancel()
			}
		}})
	if err != nil {
		return err
	}

	factory.Start(ctx.Done())
	factory.Shutdown()
	if !hasID {
		return ctx.Err()
	}
	return nil
}

// LoadImage side-loads a OCI image tarball onto the node.
func (n *Node) LoadImage(ociPath string) error {
	oci, err := os.Open(ociPath)
	if err != nil {
		return fmt.Errorf("failed to open oci tar file: %w", err)
	}
	defer oci.Close()

	addr, err := serverPublicIP(n.Server)
	if err != nil {
		return fmt.Errorf("server %q has no public ip", n.Server.Hostname)
	}
	r, err := n.cmdRunner.run(addr, "microk8s ctr image import - ", oci)
	if err != nil {
		return fmt.Errorf("failed to load image: %w, with stderr: %s", err, r)
	}
	return nil
}

type Microk8sNodeProvisioner struct {
	cherryClient cherrygo.Client
	projectID    int
	sshKeyID     string
	cmdRunner    sshCmdRunner
}

// Provision creates a Cherry Servers server and waits for k8s to be running.
func (np Microk8sNodeProvisioner) Provision(ctx context.Context) (Node, error) {
	const userDataPath = "./testdata/init-microk8s.yaml"
	return np.provision(ctx, userDataPath)
}

// ProvisionWithMetalLB creates a Cherry Servers server and waits for k8s and metallb to be running.
func (np Microk8sNodeProvisioner) ProvisionWithMetalLB(ctx context.Context) (Node, error) {
	const userDataPathWithMetalLB = "./testdata/init-microk8s-with-metallb.yaml"
	return np.provision(ctx, userDataPathWithMetalLB)
}

// ProvisionBatch wraps provision to create n Cherry Servers servers
// in a concurrent manner.
func (np Microk8sNodeProvisioner) ProvisionBatch(ctx context.Context, n int) ([]Node, []error) {
	type p struct {
		nn  Node
		err error
	}

	nodes := make([]Node, n)
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

func (np Microk8sNodeProvisioner) provision(ctx context.Context, userDataPath string) (Node, error) {
	const timeout = 513 * time.Second

	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errors.New("node provision timeout"))
	defer cancel()

	userDataRaw, err := os.ReadFile(userDataPath)
	if err != nil {
		return Node{}, fmt.Errorf("failed to read user data file: %w", err)
	}
	userdata := base64.StdEncoding.EncodeToString(userDataRaw)

	srv, err := provisionServer(ctx, np.cherryClient, np.projectID, userdata, np.sshKeyID)
	if err != nil {
		return Node{}, fmt.Errorf("failed to provision server: %w", err)
	}

	ip, err := serverPublicIP(srv)
	if err != nil {
		return Node{}, err
	}

	backoff.ExpBackoffWithContext(func() (bool, error) {
		// Check if kube-api is reachable. Non-zero exit code will be returned if not.
		_, err = np.cmdRunner.run(ip, "microk8s kubectl get nodes --no-headers", nil)
		if err != nil {
			return false, nil
		}
		return true, nil
	}, backoff.DefaultExpBackoffConfigWithContext(ctx))

	kubeconfig, err := np.cmdRunner.run(ip, "microk8s config", nil)
	if err != nil {
		return Node{}, fmt.Errorf("failed to get k8s config for node %q: %w", srv.Hostname, err)
	}

	k8sclient, err := newK8sClient(kubeconfig)
	if err != nil {
		return Node{}, fmt.Errorf("failed to create k8s client for node %q: %w", srv.Hostname, err)
	}

	n := Node{Server: srv, cmdRunner: np.cmdRunner, K8sclient: k8sclient}
	n.addCpLabel(ctx)
	err = np.untilProvisioned(ctx, n)
	if err != nil {
		return Node{}, fmt.Errorf("node didn't reach provisioned state: %w", err)
	}
	return n, nil
}

// wait until node has provider ID or is tainted with
// 'node.cloudprovider.kubernetes.io/uninitialized'
func (np Microk8sNodeProvisioner) untilProvisioned(ctx context.Context, n Node) error {
	const uninitTaint = "node.cloudprovider.kubernetes.io/uninitialized"
	ctx, cancel := context.WithTimeout(ctx, informerTimeout)
	provisioned := false

	factory := informers.NewSharedInformerFactory(n.K8sclient, informerResyncPeriod)
	_, err := factory.Core().V1().Nodes().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, newObj any) {
			newNode, _ := newObj.(*corev1.Node)
			if newNode.Name != n.Server.Hostname {
				return
			}
			if newNode.Spec.ProviderID != "" {
				provisioned = true
				cancel()
			} else if slices.ContainsFunc(newNode.Spec.Taints, func(t corev1.Taint) bool {
				return t.Key == uninitTaint
			}) {
				provisioned = true
				cancel()
			}
		}})
	if err != nil {
		return err
	}

	factory.Start(ctx.Done())
	factory.Shutdown()
	if !provisioned {
		return ctx.Err()
	}
	return nil
}

func (np Microk8sNodeProvisioner) Cleanup() error {
	_, projectErr := np.cherryClient.Projects.Delete(np.projectID)
	sshID, convErr := strconv.Atoi(np.sshKeyID)
	_, _, sshErr := np.cherryClient.SSHKeys.Delete(sshID)
	return errors.Join(projectErr, convErr, sshErr)
}

func NewMicrok8sNodeProvisioner(testName string, projectID int, cc cherrygo.Client) (Microk8sNodeProvisioner, error) {
	// Create a SSH key signer:
	sshRunner, err := NewSSHCmdRunner()
	if err != nil {
		return Microk8sNodeProvisioner{}, fmt.Errorf("failed to create SSH runner: %v", err)
	}

	// Create SSH key on Cherry servers:
	pub := ssh.MarshalAuthorizedKey(sshRunner.Signer.PublicKey())
	pub = pub[:len(pub)-1] // strip newline
	sshKey, _, err := cc.SSHKeys.Create(&cherrygo.CreateSSHKey{
		Label: testName,
		Key:   string(pub),
	})
	if err != nil {
		return Microk8sNodeProvisioner{}, fmt.Errorf("failed to create SSH key on cherry servers: %v", err)
	}

	return Microk8sNodeProvisioner{
		cherryClient: cc,
		projectID:    projectID,
		sshKeyID:     strconv.Itoa(sshKey.ID),
		cmdRunner:    *sshRunner,
	}, nil
}

func serverPublicIP(srv cherrygo.Server) (string, error) {
	for _, ip := range srv.IPAddresses {
		if ip.Type == "primary-ip" {
			return ip.Address, nil
		}
	}
	return "", fmt.Errorf("server %d has no public ip", srv.ID)
}

func newK8sClient(kubeconfig string) (*kubernetes.Clientset, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

func provisionServer(ctx context.Context, cc cherrygo.Client, projectID int, userdata, sshkeyID string) (cherrygo.Server, error) {
	const (
		serverImage = "ubuntu_24_04_64bit"
		serverPlan  = "B1-4-4gb-80s-shared"
	)

	srv, _, err := cc.Servers.Create(&cherrygo.CreateServer{
		ProjectID: projectID,
		Plan:      serverPlan,
		Region:    Region,
		Image:     serverImage,
		UserData:  userdata,
		SSHKeys:   []string{sshkeyID},
	})

	if err != nil {
		return cherrygo.Server{}, fmt.Errorf("failed to create server: %w", err)
	}

	backoff.ExpBackoffWithContext(func() (bool, error) {
		srv, _, err = cc.Servers.Get(srv.ID, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get server: %w", err)
		}
		if srv.State == "active" {
			return true, nil
		}
		return false, nil
	}, backoff.DefaultExpBackoffConfigWithContext(ctx))

	return srv, nil
}
