package e2etest

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"

	"github.com/cherryservers/cherrygo/v3"
	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/kubernetes"

	ccm "github.com/cherryservers/cloud-provider-cherry/cherry"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	metallbSetting = "metallb:///"
	kubeVipSetting = "kube-vip://"
)

func setupProject(t testing.TB, name string) cherrygo.Project {
	t.Helper()

	project, _, err := cherryClient.Projects.Create(*teamID, &cherrygo.CreateProject{
		Name: name})
	if err != nil {
		t.Fatalf("failed to setup cherry servers project: %v", err)
	}
	t.Cleanup(func() {
		cherryClient.Projects.Delete(project.ID)
	})
	return project
}

func setupMicrok8sNodeProvisioner(t testing.TB, testName string, projectID int) microk8sNodeProvisioner {
	t.Helper()

	// Create a SSH key signer:
	sshRunner, err := newSshCmdRunner()
	if err != nil {
		t.Fatalf("failed to create SSH runner: %v", err)
	}

	// Create SSH key on Cherry servers:
	pub := ssh.MarshalAuthorizedKey(sshRunner.signer.PublicKey())
	pub = pub[:len(pub)-1] // strip newline
	sshKey, _, err := cherryClient.SSHKeys.Create(&cherrygo.CreateSSHKey{
		Label: testName,
		Key:   string(pub),
	})
	if err != nil {
		t.Fatalf("failed to create SSH key on cherry servers: %v", err)
	}
	t.Cleanup(func() {
		cherryClient.SSHKeys.Delete(sshKey.ID)
	})
	return microk8sNodeProvisioner{
		cherryClient: *cherryClient,
		projectID:    projectID,
		sshKeyID:     strconv.Itoa(sshKey.ID),
		cmdRunner:    *sshRunner,
	}
}

func setupMicrok8sMetalLBNodeProvisioner(t testing.TB, testName string, projectID int) microk8sMetalLBNodeProvisioner {
	p := setupMicrok8sNodeProvisioner(t, testName, projectID)
	return microk8sMetalLBNodeProvisioner{microk8sNodeProvisioner: p}
}

func setupKubeConfig(t testing.TB, n node) string {
	t.Helper()

	cfg, cleanup, err := n.kubeconfig()
	if err != nil {
		t.Fatalf("failed to generate kubeconfig: %v", err)
	}
	t.Cleanup(func() {
		cleanup()
	})
	return cfg
}

func setupCcmSecret(t testing.TB, ccmCfg ccm.Config) string {
	t.Helper()

	secret, cleanup, err := ccmSecret(ccmCfg)
	if err != nil {
		t.Fatalf("failed to setup ccm secret config")
	}
	t.Cleanup(func() {
		cleanup()
	})
	return secret
}

type testEnv struct {
	project   cherrygo.Project
	mainNode  node
	nodeProvisioner nodeProvisioner
	k8sClient kubernetes.Interface
}

type testEnvConfig struct {
	name         string
	loadBalancer string // optional
	fipTag       string // optional
}

func setupTestEnv(ctx context.Context, t testing.TB, cfg testEnvConfig) *testEnv {
	t.Helper()

	// Setup project:
	project := setupProject(t, cfg.name)

	// Setup node provisioner:
	var np nodeProvisioner
	if cfg.loadBalancer != metallbSetting {
		np = setupMicrok8sNodeProvisioner(t, cfg.name, project.ID)
	} else {
		np = setupMicrok8sMetalLBNodeProvisioner(t, cfg.name, project.ID)
	}

	// Create a node (server with k8s running):
	node, err := np.Provision(t.Context())
	if err != nil {
		t.Fatalf("failed to provision test node: %v", err)
	}

	// Get node kubeconfig:
	kubeCfg := setupKubeConfig(t, *node)

	client, err := newK8sClient(kubeCfg)
	if err != nil {
		t.Fatalf("failed to create k8s client: %v", err)
	}

	// Generate config secret for CCM:
	secret := setupCcmSecret(t, ccm.Config{
		AuthToken:           cherryClient.AuthToken,
		Region:              region,
		LoadBalancerSetting: cfg.loadBalancer,
		FIPTag:              cfg.fipTag,
		ProjectID:           project.ID})

	// Cancel child process on interrupt/termination.
	// Should work on Windows as well, see https://pkg.go.dev/os/signal#hdr-Windows.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	// Launch CCM:
	stopped, err := runCcm(ctx, kubeCfg, secret, client)
	if err != nil {
		t.Fatalf("failed to run CCM: %v", err)
	}
	// Stop signal diversion when CCM is stopped.
	go func() {
		<-stopped
		stop()
	}()
	t.Cleanup(func() {
		<-stopped
	})

	return &testEnv{
		project:   project,
		mainNode:  *node,
		k8sClient: client,
		nodeProvisioner: np,
	}
}

func newK8sClient(kubeconfig string) (*kubernetes.Clientset, error) {
	cfg,err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build k8s config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build k8s clientset: %w", err)
	}
	return clientset, nil
}