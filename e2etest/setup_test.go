package e2etest

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"strconv"
	"testing"

	"github.com/cherryservers/cherrygo/v3"
	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/cherryservers/cloud-provider-cherry-tests/node"
	ccm "github.com/cherryservers/cloud-provider-cherry/cherry"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
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

	err = n.LoadImage(ctx, "../dist/bin/ccm-test.tar")
	if err != nil {
		t.Fatalf("failed to load image to node")
	}

	client := n.K8sclient

	configJSON, err := json.Marshal(ccm.Config{
		AuthToken:           cherryClient.AuthToken,
		Region:              node.Region,
		LoadBalancerSetting: cfg.loadBalancer,
		FIPTag:              cfg.fipTag,
		ProjectID:           project.ID})

	if err != nil {
		t.Fatalf("failed to marshall ccm config: %v", err)
	}

	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cherry-cloud-config", Namespace: metav1.NamespaceSystem},
		StringData: map[string]string{"cloud-sa.json": string(configJSON)},
	}

	client.CoreV1().Secrets(metav1.NamespaceSystem).Create(ctx, &secret, metav1.CreateOptions{})

	manifest, err := os.ReadFile("./testdata/ccm-manifest.yaml")
	if err != nil {
		t.Fatalf("failed to read manifest: %v", err)
	}

	r, err := n.RunCmdWithInput("microk8s kubectl apply -f - ", manifest)
	if err != nil {
		t.Fatalf("failed to apply manifest: %s", r)
	}

	// when node.cloudprovider.kubernetes.io/uninitialized
	// is gone, the ccm is running.
	untilNodeUntainted(ctx, t, client)

	return &testEnv{
		project:         project,
		mainNode:        n,
		k8sClient:       client,
		nodeProvisioner: np,
	}
}

func untilNodeUntainted(ctx context.Context, t testing.TB, client kubernetes.Interface) {
	const informerResyncPeriod = 5 * time.Second
	done := make(chan struct{})

	factory := informers.NewSharedInformerFactory(client, informerResyncPeriod)
	_, err := factory.Core().V1().Nodes().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, newObj any) {
			newNode, _ := newObj.(*corev1.Node)
			if len(newNode.Spec.Taints) == 0 {
				select {
				case <-done:
				default:
					close(done)
				}
			}
		}})
	if err != nil {
		t.Fatalf("failed to add node event handler: %v", err)
	}

	factory.Start(done)
	factory.WaitForCacheSync(ctx.Done())
	select {
	case <-done:
		factory.Shutdown()
	case <-ctx.Done():
		t.Fatalf("timed out waiting for node to become untainted: %v", ctx.Err())
	}
}
