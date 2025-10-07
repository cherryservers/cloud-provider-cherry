package test

import (
	"strconv"
	"testing"

	"github.com/cherryservers/cherrygo/v3"
	"golang.org/x/crypto/ssh"
)

func TestKubeVip(t *testing.T) {
	// Things we need to do:
	// 1. Create a project(BGP disabled, to test BGP enabler).
	// 2. Create a local SSH key signer.
	// 3. Create a SSH key on Cherry Servers.
	// 4. Create a server on Cherry Servers.
	// 5. Ensure K8S is running on our server.
	// 6. Deploy kube-vip on our server/node.
	// 7. Start the CCM locally.
	// 8. ***Do the tests***

	// Create a project:
	const testName = "kubernetes-ccm-test-lb-kube-vip"
	ctx := t.Context()
	
	project, _, err := cherryClientFixture.Projects.Create(*teamIDFixture, &cherrygo.CreateProject{
		Name: testName})
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	// defer cherryClientFixture.Projects.Delete(project.ID)

	// Create a SSH key signer:
	sshRunner, err := newSshCmdRunner()
	if err != nil {
		t.Fatalf("failed to create SSH runner: %v", err)
	}

	// Create SSH key on Cherry servers:
	pub := ssh.MarshalAuthorizedKey(sshRunner.signer.PublicKey())
	pub = pub[:len(pub)-1] // strip newline
	sshKey, _, err := cherryClientFixture.SSHKeys.Create(&cherrygo.CreateSSHKey{
		Label: testName,
		Key:   string(pub),
	})
	if err != nil {
		t.Fatalf("failed to create SSH key on cherry servers: %v", err)
	}
	defer cherryClientFixture.SSHKeys.Delete(sshKey.ID)

	np := newNodeProvisioner(*cherryClientFixture, project.ID, strconv.Itoa(sshKey.ID), *sshRunner)

	// Create a node (server with k8s running):
	node, err := np.provision(ctx)
	if err != nil {
		t.Fatalf("failed to provision test node: %v", err) 
	}

	// Generate client for node's kube-api:
	cfg, cleanup, err := node.kubeconfig()
	if err != nil {
		t.Fatalf("failed to generate node kubeconfig: %v", err)
	}
	defer cleanup()

	client, err := k8sClient(cfg)
	if err != nil {
		t.Fatalf("failed to create k8s client: %v", err)
	}

	// Generate config secret for CCM:
	secret, cleanup, err := ccmSecret(cherryClientFixture.AuthToken, region, "", "kube-vip://", project.ID)
	if err != nil {
		t.Fatalf("failed to create config secret for ccm: %v", err)
	}
	defer cleanup()

	// Launch CCM:
	runCcm(ctx, cfg, secret, client)
	
}