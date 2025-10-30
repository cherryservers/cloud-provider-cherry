package e2e_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/cherryservers/cherrygo/v3"
	ccm "github.com/cherryservers/cloud-provider-cherry/cherry"
	"golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/watch"
)

type backoffStoppedError struct{}

func (e backoffStoppedError) Error() string {
	return "exp backoff stopped"
}

type ExpBackoffConfig struct {
	start time.Duration
	exp   int
	max   time.Duration
}

func defaultExpBackoffConfig() ExpBackoffConfig {
	return ExpBackoffConfig{
		start: 2 * time.Second,
		exp:   2,
		max:   32 * time.Second,
	}
}

// expBackoff retries f until it returns true, a non-nil error or
// stop returns true.
func expBackoff(f func() (bool, error), cfg ExpBackoffConfig, stop func() bool) error {
	dur := cfg.start
	for !stop() {
		r, err := f()
		if err != nil || r {
			return err
		}
		time.Sleep(dur + time.Duration(rand.Intn(100)+100)*time.Millisecond)
		dur *= time.Duration(cfg.exp)
		if dur > cfg.max {
			dur = cfg.max
		}
	}
	return backoffStoppedError{}
}

type ExpBackoffConfigWithContext struct {
	ExpBackoffConfig
	ctx context.Context
}

func defaultExpBackoffConfigWithContext(ctx context.Context) ExpBackoffConfigWithContext {
	return ExpBackoffConfigWithContext{ExpBackoffConfig: defaultExpBackoffConfig(), ctx: ctx}
}

// expBackoffWithContext wraps expBackoff with a context.
func expBackoffWithContext(f func() (bool, error), cfg ExpBackoffConfigWithContext) error {
	err := expBackoff(f, cfg.ExpBackoffConfig, func() bool {
		return cfg.ctx.Err() != nil
	})
	switch {
	case errors.Is(err, backoffStoppedError{}):
		return fmt.Errorf("exp backoff context cancelled %w", cfg.ctx.Err())
	case err != nil:
		return fmt.Errorf("exp backoff cancelled: %w", err)
	default:
		return nil
	}
}

func fipCount(fips []cherrygo.IPAddress) int {
	count := 0
	for _, fip := range fips {
		if fip.Type == "floating-ip" {
			count++
		}
	}
	return count
}

type sshCmdRunner struct {
	signer ssh.Signer
}

// run a command via SSH at the given address using bash.
// On a non-zero exit code, the response string contains stderr.
func (s sshCmdRunner) run(addr, cmd string) (string, error) {
	const port = "22"

	cfg := ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(s.signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := ssh.Dial("tcp", addr+":"+port, &cfg)
	if err != nil {
		return "", fmt.Errorf("failed ssh dial: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to establish session: %w", err)
	}
	defer session.Close()

	var b bytes.Buffer
	var eb bytes.Buffer
	session.Stdout = &b
	session.Stderr = &eb
	if err := session.Run("bash -lc " + strconv.Quote(cmd)); err != nil {
		return eb.String(), fmt.Errorf("failed to run cmd: %w", err)
	}

	return b.String(), nil
}

func newSshCmdRunner() (*sshCmdRunner, error) {
	_, pri, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 keys: %w", err)
	}

	sig, err := ssh.NewSignerFromSigner(pri)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key signer: %w", err)
	}

	s := sshCmdRunner{sig}
	return &s, nil
}

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

// runCcm runs the CCM using the go toolchain as a child process.
// The child process is cancelled when the context is cancelled,
// but has a teardown process, which is done when `stopped` is closed.
func runCcm(ctx context.Context, kubeconfig, secret string, k8sClient kubernetes.Interface) (stopped <-chan struct{}, err error) {
	cmd := exec.CommandContext(ctx, "go", "run", "..", "--cloud-provider=cherryservers",
		"--leader-elect=false", "--authentication-skip-lookup=true",
		"--kubeconfig="+kubeconfig, "--cloud-config="+secret, "--v=2")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Println("failed to create stderr pipe for ccm process")
	} else {
		go io.Copy(log.Writer(), stderr)
	}

	// Need this for metallb client, which doesn't build
	// from the controller client builder and doesn't get the
	// flags from the command line.
	env := append(os.Environ(), fmt.Sprintf("KUBECONFIG=%s", kubeconfig))
	cmd.Env = env

	err = cmd.Start()
	if err != nil {
		return nil, err
	}
	log.Printf("ccm pid: %d\n", cmd.Process.Pid)

	// Ensure graceful exit on teardown.
	stoppedCh := make(chan struct{})
	go func() {
		<-ctx.Done()
		cmd.Wait()
		close(stoppedCh)
	}()

	informerCtx, cancel := context.WithCancel(ctx)

	factory := informers.NewSharedInformerFactory(k8sClient, resyncPeriod)
	_, err = factory.Core().V1().Nodes().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj any) {
			newNode, _ := newObj.(*corev1.Node)
			// if there's no taints, the node was successfully registered by the ccm
			if len(newNode.Spec.Taints) == 0 {
				log.Printf("reached 0 taints for node %s\n", newNode.ObjectMeta.Name)
				cancel()
			}
		}})
	if err != nil {
		return stoppedCh, fmt.Errorf("failed to add node event handler: %w", err)
	}

	factory.Start(informerCtx.Done())
	factory.WaitForCacheSync(informerCtx.Done())
	<-informerCtx.Done()
	factory.Shutdown()

	return stoppedCh, nil
}

// ccmSecret generates the secret required for CCM deployment
// and returns a path to a temp file with it.
func ccmSecret(cfg ccm.Config) (path string, cleanup func(), err error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshall secret to json: %w", err)
	}

	f, err := os.CreateTemp("", "ccm-secret-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp file for secret: %w", err)
	}
	path = f.Name()
	cleanup = fileCleanup(path)

	_, err = f.Write(data)
	if err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("failed to write secret to file: %w", err)
	}

	err = f.Close()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to close secret file: %w", err)
	}

	return path, cleanup, nil
}
