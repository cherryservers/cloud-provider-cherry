package test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	cherrygo "github.com/cherryservers/cherrygo/v3"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	apiTokenVar  = "CHERRY_TEST_API_TOKEN"
	teamIDVar    = "CHERRY_TEST_TEAM_ID"
	serverImage  = "ubuntu_24_04_64bit"
	serverPlan   = "B1-4-4gb-80s-shared"
	region       = "LT-Siauliai"
	userDataPath = "./testdata/cloud-config/init-microk8s.yaml"
	resyncPeriod = 5 * time.Second
)

var cherryClientFixture *cherrygo.Client
var k8sClientFixture kubernetes.Interface
var cpNodeFixture *node

type config struct {
	apiToken string
	teamID   int
}

// loadConfig loads test configuration from environment variables.
func loadConfig() (config, error) {
	apiToken := os.Getenv(apiTokenVar)
	teamID, err := strconv.Atoi(os.Getenv(teamIDVar))
	if err != nil {
		return config{}, fmt.Errorf("failed to parse project ID: %w", err)
	}
	return config{apiToken, teamID}, nil
}

// cherryClient initializes cherrygo client fixture.
func cherryClient(apiToken string) error {
	var err error
	cherryClientFixture, err = cherrygo.NewClient(cherrygo.WithAuthToken(apiToken))
	if err != nil {
		return fmt.Errorf("failed to initialize cherrygo client: %w", err)
	}
	return nil
}

// k8sClient initializes k8s clientset fixture.
func k8sClient(kubeconfig string) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("Failed to build k8s config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("Failed to build k8s clientset: %w", err)
	}
	k8sClientFixture = clientset
	return nil
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
	server    cherrygo.Server
	cmdRunner sshCmdRunner
}

// runCmd runs a shell command on the node via SSH.
func (n node) runCmd(cmd string) (resp string, err error) {
	ip, err := serverPublicIP(n.server)
	if err != nil {
		return "", err
	}
	return n.cmdRunner.run(ip, cmd)
}

// kubeconfig generates a kubeconfig file from the node
// and returns a path to it.
func (n node) kubeconfig() (path string, cleanup func(), err error) {
	const cmd = "microk8s config"
	if err != nil {
		return "", nil, fmt.Errorf("CP node has no public IP: %w", err)
	}
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

	return path, cleanup, nil
}

type nodeProvisioner struct {
	cherryClient cherrygo.Client
	projectID    int
	sshKeyID     string
	cmdRunner    sshCmdRunner
}

// provision creates a Cherry Servers server and waits for k8s to be running.
func (np nodeProvisioner) provision() (*node, error) {
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

	expBackoff(func() (bool, error) {
		srv, _, err = np.cherryClient.Servers.Get(srv.ID, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get server: %w", err)
		}
		if srv.State == "active" {
			return true, nil
		}
		return false, nil
	}, defaultExpBackoffConfig())

	ip, err := serverPublicIP(srv)
	if err != nil {
		return nil, err
	}

	expBackoff(func() (bool, error) {
		// Check if kube-api is reachable. Non-zero exit code will be returned if not.
		_, err = np.cmdRunner.run(ip, "microk8s kubectl get nodes --no-headers")
		if err != nil {
			return false, nil
		}
		return true, nil
	}, defaultExpBackoffConfig())

	return &node{srv, np.cmdRunner}, nil
}

func serverPublicIP(srv cherrygo.Server) (string, error) {
	for _, ip := range srv.IPAddresses {
		if ip.Type == "primary-ip" {
			return ip.Address, nil
		}
	}
	return "", fmt.Errorf("server %d has no public ip", srv.ID)
}

func fileCleanup(path string) func() {
	var once sync.Once
	return func() {
		once.Do(func() { os.Remove(path) })
	}
}

// ccmSecret generates the secret required for CCM deployment
// and returns a path to a temp file with it.
func ccmSecret(apiToken, region string, projectID int) (path string, cleanup func(), err error) {
	s := struct {
		APIKey    string `json:"apiKey"`
		ProjectID int    `json:"projectId"`
		Region    string `json:"region,omitempty"`
	}{apiToken, projectID, region}

	data, err := json.Marshal(s)
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

// runCcm runs the CCM using the go toolchain as a child process.
// This child process is cancelled on interrupt or termination,
// but the caller is responsible for cleanup in normal program flow.
func runCcm(ctx context.Context, kubeconfig, secret string, k8sClient kubernetes.Interface) (cleanup func(), err error) {
	cmd := exec.CommandContext(ctx, "go", "run", "..", "--cloud-provider=cherryservers",
		"--leader-elect=false", "--authentication-skip-lookup=true",
		"--kubeconfig="+kubeconfig, "--cloud-config="+secret)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Println("failed to create stderr pipe for ccm process")
	} else {
		go func() {
			io.Copy(log.Writer(), stderr)
		}()
	}

	err = cmd.Start()
	if err != nil {
		return nil, err
	}
	log.Printf("ccm pid: %d\n", cmd.Process.Pid)

	cleanup = func() {
		cmd.Cancel()
		cmd.Wait()
	}

	// Cancel child process on interrupt/termination.
	// Should work on Windows as well, see https://pkg.go.dev/os/signal#hdr-Windows.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-ctx.Done()
		cleanup()
		stop()
	}()

	ctx, cancel := context.WithCancel(ctx)

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
		return nil, fmt.Errorf("failed to add node event handler: %w", err)
	}

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())
	<-ctx.Done()
	factory.Shutdown()

	return cleanup, nil
}

func runMain(ctx context.Context, m *testing.M) (code int, err error) {
	cfg, err := loadConfig()
	if err != nil {
		return 1, fmt.Errorf("failed to load test config: %w", err)
	}

	err = cherryClient(cfg.apiToken)
	if err != nil {
		return 1, err
	}

	sshRunner, err := newSshCmdRunner()
	if err != nil {
		return 1, fmt.Errorf("failed to create SSH runner: %w", err)
	}

	pub := ssh.MarshalAuthorizedKey(sshRunner.signer.PublicKey())
	pub = pub[:len(pub)-1] // strip newline
	sshKey, _, err := cherryClientFixture.SSHKeys.Create(&cherrygo.CreateSSHKey{
		Label: "kubernetes-ccm-test",
		Key:   string(pub),
	})
	if err != nil {
		return 1, fmt.Errorf("failed to create SSH key on cherry servers: %w", err)
	}
	defer cherryClientFixture.SSHKeys.Delete(sshKey.ID)

	project, _, err := cherryClientFixture.Projects.Create(cfg.teamID, &cherrygo.CreateProject{
		Name: "kubernetes-ccm-test", Bgp: true})
	if err != nil {
		return 1, fmt.Errorf("failed to create project: %w", err)
	}
	defer cherryClientFixture.Projects.Delete(project.ID)

	np := nodeProvisioner{*cherryClientFixture, project.ID, strconv.Itoa(sshKey.ID), *sshRunner}
	cpNode, err := np.provision()
	if err != nil {
		return 1, fmt.Errorf("failed to provision k8s control plane node: %w", err)
	}
	cpNodeFixture = cpNode

	kubeconfig, cleanup, err := cpNode.kubeconfig()
	if err != nil {
		return 1, fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	defer cleanup()

	err = k8sClient(kubeconfig)
	if err != nil {
		return 1, fmt.Errorf("failed to initialize k8s clientset: %w", err)
	}

	secret, cleanup, err := ccmSecret(cfg.apiToken, region, project.ID)
	if err != nil {
		return 1, fmt.Errorf("failed to generate secret for ccm: %w", err)
	}
	defer cleanup()


	cleanup, err = runCcm(ctx, kubeconfig, secret, k8sClientFixture)
	if err != nil {
		return 1, fmt.Errorf("failed to start ccm: %w", err)
	}
	defer cleanup()

	code = m.Run()
	return code, nil
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	code, err := runMain(ctx, m)
	if err != nil {
		log.Fatal(err)
	}

	os.Exit(code)

}
