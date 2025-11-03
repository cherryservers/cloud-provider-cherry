package e2etest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"

	"strconv"
	"testing"

	"github.com/cherryservers/cherrygo/v3"
	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/kubernetes"

	"github.com/cherryservers/cloud-provider-cherry-tests/node"
	ccm "github.com/cherryservers/cloud-provider-cherry/cherry"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		//cherryClient.Projects.Delete(project.ID)
	})
	return project
}

func setupMicrok8sNodeProvisioner(t testing.TB, testName string, projectID int) node.Microk8sNodeProvisioner {
	t.Helper()

	// Create a SSH key signer:
	sshRunner, err := node.NewSshCmdRunner()
	if err != nil {
		t.Fatalf("failed to create SSH runner: %v", err)
	}

	// Create SSH key on Cherry servers:
	pub := ssh.MarshalAuthorizedKey(sshRunner.Signer.PublicKey())
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
	return node.Microk8sNodeProvisioner{
		CherryClient: *cherryClient,
		ProjectID:    projectID,
		SshKeyID:     strconv.Itoa(sshKey.ID),
		CmdRunner:    *sshRunner,
	}
}

func setupMicrok8sMetalLBNodeProvisioner(t testing.TB, testName string, projectID int) node.Microk8sMetalLBNodeProvisioner {
	p := setupMicrok8sNodeProvisioner(t, testName, projectID)
	return node.Microk8sMetalLBNodeProvisioner{Microk8sNodeProvisioner: p}
}

type testEnv struct {
	project         cherrygo.Project
	mainNode        node.Node
	nodeProvisioner node.NodeProvisioner
	k8sClient       kubernetes.Interface
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
	var np node.NodeProvisioner
	if cfg.loadBalancer != metallbSetting {
		np = setupMicrok8sNodeProvisioner(t, cfg.name, project.ID)
	} else {
		np = setupMicrok8sMetalLBNodeProvisioner(t, cfg.name, project.ID)
	}

	// Create a node (server with k8s running):
	n, err := np.Provision(t.Context())
	if err != nil {
		t.Fatalf("failed to provision test node: %v", err)
	}

	err = n.LoadImage(ctx, *ccmImagePath)
	if err != nil {
		t.Fatalf("failed to load image to node")
	}

	deployCcm(ctx, t, n, ccm.Config{
		AuthToken:           cherryClient.AuthToken,
		Region:              node.Region,
		LoadBalancerSetting: cfg.loadBalancer,
		FIPTag:              cfg.fipTag,
		ProjectID:           project.ID})

	return &testEnv{
		project:         project,
		mainNode:        n,
		k8sClient:       n.K8sclient,
		nodeProvisioner: np,
	}
}

func deployCcm(ctx context.Context, t testing.TB, n node.Node, cfg ccm.Config) {
	const imgTag = "ghcr.io/cherryservers/cloud-provider-cherry:test"

	configJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("failed to marshall ccm config: %v", err)
	}

	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cherry-cloud-config", Namespace: metav1.NamespaceSystem},
		StringData: map[string]string{"cloud-sa.json": string(configJSON)},
	}

	n.K8sclient.CoreV1().Secrets(metav1.NamespaceSystem).Create(ctx, &secret, metav1.CreateOptions{})

	manifest, err := os.ReadFile("../deploy/template/deployment.yaml")
	if err != nil {
		t.Fatalf("failed to read deployment manifest file: %v", err)
	}
	manifest = bytes.Replace(manifest, []byte("RELEASE_IMG"), []byte(imgTag), 1)
	manifest = bytes.Replace(
		manifest,
		[]byte("imagePullPolicy: Always"),
		[]byte("imagePullPolicy: Never"), 1)

	r, err := n.RunCmd("microk8s kubectl apply -f - ", bytes.NewReader(manifest))
	if err != nil {
		t.Fatalf("failed to apply manifest: %s", r)
	}

	// when node.cloudprovider.kubernetes.io/uninitialized
	// is gone, the ccm is running.
	n.UntilNodeUntainted(ctx)
}
