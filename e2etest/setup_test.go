package e2etest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"

	"testing"

	"github.com/cherryservers/cherrygo/v3"
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
	return project
}

type testEnv struct {
	project         cherrygo.Project
	mainNode        node.Node
	nodeProvisioner node.Microk8sNodeProvisioner
	k8sClient       kubernetes.Interface
}

type testEnvConfig struct {
	name         string
	loadBalancer string // optional
	fipTag       string // optional
}

type nodeProvisioner interface {
	Provision(context.Context) (node.Node, error)
}

type batchNodeProvisioner interface {
	ProvisionBatch(context.Context, int) ([]node.Node, []error)
}

func setupTestEnv(ctx context.Context, t testing.TB, cfg testEnvConfig) *testEnv {
	t.Helper()

	// Setup project:
	project := setupProject(t, cfg.name)

	// Setup node provisioner:
	np, err := node.NewMicrok8sNodeProvisioner(cfg.name, project.ID, *cherryClient)
	if err != nil {
		t.Fatalf("failed to setup node provisioner: %v", err)
	}
	t.Cleanup(func() {
		np.Cleanup()
	})

	// Create a node (server with k8s running):
	var n node.Node
	if cfg.loadBalancer != metallbSetting {
		n, err = np.Provision(t.Context())
	} else {
		n, err = np.ProvisionWithMetalLB(t.Context())
	}
	if err != nil {
		t.Fatalf("failed to provision test node: %v", err)
	}

	err = n.LoadImage(*ccmImagePath)
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
	const (
		imgTag       = "ghcr.io/cherryservers/cloud-provider-cherry:test"
		manifestPath = "../deploy/template/deployment.yaml"
	)

	configJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("failed to marshall ccm config: %v", err)
	}

	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cherry-cloud-config", Namespace: metav1.NamespaceSystem},
		StringData: map[string]string{"cloud-sa.json": string(configJSON)},
	}

	n.K8sclient.CoreV1().Secrets(metav1.NamespaceSystem).Create(ctx, &secret, metav1.CreateOptions{})

	manifest, err := os.ReadFile(manifestPath)
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
	n.UntilHasProviderID(ctx)
}
