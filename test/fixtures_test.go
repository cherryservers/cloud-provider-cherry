package e2e_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	cherrygo "github.com/cherryservers/cherrygo/v3"
	ccm "github.com/cherryservers/cloud-provider-cherry/cherry"
	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	apiTokenVar             = "CHERRY_TEST_API_TOKEN"
	teamIDVar               = "CHERRY_TEST_TEAM_ID"
	serverImage             = "ubuntu_24_04_64bit"
	serverPlan              = "B1-4-4gb-80s-shared"
	region                  = "LT-Siauliai"
	fipTag                  = "kubernetes-ccm-test"
	userDataPath            = "./testdata/cloud-config/init-microk8s.yaml"
	userDataPathWithMetalLB = "./testdata/cloud-config/init-microk8s-with-metallb.yaml"
	resyncPeriod            = 5 * time.Second
	eventTimeout            = 90 * time.Second
	provisionTimeout        = 513 * time.Second
	joinTimeout             = 210 * time.Second
	controlPlaneNodeLabel   = "node-role.kubernetes.io/control-plane"
)

var cherryClientFixture *cherrygo.Client
var k8sClientFixture kubernetes.Interface
var nodeProvisionerFixture manyNodeProvisioner
var cpNodeFixture *node
var fipFixture *cherrygo.IPAddress
var teamIDFixture *int

type config struct {
	apiToken string
	teamID   int
}

// loadConfig loads test configuration from environment variables.
func loadConfig() (config, error) {
	apiToken := os.Getenv(apiTokenVar)
	teamID, err := strconv.Atoi(os.Getenv(teamIDVar))
	if err != nil {
		return config{}, fmt.Errorf("failed to parse team ID: %w", err)
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

func k8sClient(kubeconfig string) (*kubernetes.Clientset, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build k8s config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build k8s clientset: %w", err)
	}
	return clientset, nil
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

func runMain(ctx context.Context, m *testing.M) (code int, err error) {
	cfg, err := loadConfig()
	if err != nil {
		return 1, fmt.Errorf("failed to load test config: %w", err)
	}
	teamIDFixture = &cfg.teamID

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
	//defer cherryClientFixture.Projects.Delete(project.ID)

	np := microk8sNodeProvisioner{
		*cherryClientFixture, project.ID, strconv.Itoa(sshKey.ID), *sshRunner,
	}
	cpNode, err := np.Provision(ctx)
	if err != nil {
		return 1, fmt.Errorf("failed to provision k8s control plane node: %w", err)
	}
	nodeProvisionerFixture = np
	cpNodeFixture = cpNode

	kubeconfig, cleanup, err := cpNode.kubeconfig()
	if err != nil {
		return 1, fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	defer cleanup()

	k8sClientFixture, err = k8sClient(kubeconfig)
	if err != nil {
		return 1, fmt.Errorf("failed to initialize k8s clientset: %w", err)
	}

	tags := map[string]string{fipTag: ""}
	fip, _, err := cherryClientFixture.IPAddresses.Create(
		project.ID, &cherrygo.CreateIPAddress{Region: region, Tags: &tags})
	if err != nil {
		return 1, fmt.Errorf("failed to create fip: %w", err)
	}
	fipFixture = &fip

	secret, cleanup, err := ccmSecret(ccm.Config{
		AuthToken: cfg.apiToken, Region: region, FIPTag: fipTag, ProjectID: project.ID,
	})
	if err != nil {
		return 1, fmt.Errorf("failed to generate secret for ccm: %w", err)
	}
	defer cleanup()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	stoppedCh, err := runCcm(ctx, kubeconfig, secret, k8sClientFixture)
	if err != nil {
		return 1, fmt.Errorf("failed to start ccm: %w", err)
	}
	go func() {
		<-stoppedCh
		stop()
	}()

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
