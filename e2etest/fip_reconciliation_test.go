package e2etest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cherryservers/cherrygo/v3"
	"github.com/cherryservers/cloud-provider-cherry-tests/backoff"
	"github.com/cherryservers/cloud-provider-cherry-tests/node"
)

func untilIPHasTarget(ctx context.Context, ip cherrygo.IPAddress, target string) error {
	const timeout = 180 * time.Second
	ctx, cancel := context.WithTimeoutCause(
		ctx, timeout, errors.New("timeout out waiting for ip to get target"))
	defer cancel()

	return backoff.ExpBackoffWithContext(func() (bool, error) {
		fip, _, err := cherryClient.IPAddresses.Get(ip.ID, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get fip: %w", err)
		}
		if fip.TargetedTo.Hostname == target {
			return true, nil
		}
		return false, nil
	}, backoff.DefaultExpBackoffConfigWithContext(ctx))
}

func TestFipControlPlaneReconciliation(t *testing.T) {
	t.Parallel()
	const fipTag = "kubernetes-ccm-test"
	ctx := t.Context()

	cfg := testEnvConfig{name: "kubernetes-ccm-test-fip-controlplane", fipTag: fipTag}
	env := setupTestEnv(ctx, t, cfg)
	var np batchNodeProvisioner = env.nodeProvisioner

	fip, _, err := cherryClient.IPAddresses.Create(
		env.project.ID, &cherrygo.CreateIPAddress{
			Region: node.Region,
			Tags:   &map[string]string{fipTag: ""}})

	if err != nil {
		t.Fatalf("failed to create cherry servers fip: %v", err)
	}

	err = untilIPHasTarget(ctx, fip, env.mainNode.Server.Hostname)
	if err != nil {
		t.Fatalf("fip didn't get attached to cp node: %v", err)
	}

	nodes, errs := np.ProvisionBatch(ctx, 2)
	for _, err := range errs {
		if err != nil {
			t.Fatalf("failed to provision node: %v", err)
		}
	}
	cp1 := nodes[0]
	cp2 := nodes[1]

	errs = env.mainNode.JoinBatch(ctx, []node.Node{cp1, cp2})
	for _, err := range errs {
		if err != nil {
			t.Fatalf("failed to join node: %v", err)
		}
	}

	// test that fip remains attached to node after deleting another node
	_, _, err = cherryClient.Servers.Delete(cp1.Server.ID)
	if err != nil {
		t.Fatalf("failed to delete server: %q", cp1.Server.Hostname)
	}

	fip, _, err = cherryClient.IPAddresses.Get(fip.ID, nil)
	if err != nil {
		t.Fatalf("failed to get fip: %v", err)
	}
	if fip.TargetedTo.Hostname != env.mainNode.Server.Hostname {
		t.Fatalf("fip target: %q, want %q", fip.TargetedTo.Hostname, env.mainNode.Server.Hostname)
	}

	// test that fip is reattached when a cp node is disabled
	err = cp2.Remove(&env.mainNode)
	if err != nil {
		t.Fatalf("couldn't remove node from cluster: %v", err)
	}

	err = untilIPHasTarget(ctx, fip, cp2.Server.Hostname)
	if err != nil {
		t.Fatalf("fip didn't get attached to cp node: %v", err)
	}

}
