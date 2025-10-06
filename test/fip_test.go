package test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/cherryservers/cherrygo/v3"
)

func untilIpHasTarget(ctx context.Context, ip cherrygo.IPAddress, target string) error {
	ctx, cancel := context.WithTimeoutCause(ctx, eventTimeout*2, errors.New("timeout for until ip has target"))
	defer cancel()

	return expBackoffWithContext(func() (bool, error) {
		fip, _, err := cherryClientFixture.IPAddresses.Get(fipFixture.ID, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get fip: %w", err)
		}
		if fip.TargetedTo.Hostname == target {
			return true, nil
		}
		return false, nil
	}, defaultExpBackoffConfigWithContext(ctx))
}

func TestControlPlaneFip(t *testing.T) {
	ctx := t.Context()

	err := untilIpHasTarget(ctx, *fipFixture, cpNodeFixture.server.Hostname)
	if err != nil {
		t.Fatalf("fip didn't get attached to cp node: %v", err)
	}

	nodes, errs := nodeProvisionerFixture.provisionMany(ctx, 2)
	for _, err := range errs {
		if err != nil {
			t.Fatalf("failed to provision node: %v", err)
		}
	}
	cp1 := *nodes[0]
	cp2 := *nodes[1]

	errs = cpNodeFixture.joinMany(ctx, []node{cp1, cp2}, k8sClientFixture)
	for _, err := range errs {
		if err != nil {
			t.Fatalf("failed to join node: %v", err)
		}
	}

	// test that fip remains attached to node after deleting another node
	_, _, err = cherryClientFixture.Servers.Delete(cp1.server.ID)
	if err != nil {
		t.Fatalf("failed to delete server: %q", cp1.server.Hostname)
	}

	fip, _, err := cherryClientFixture.IPAddresses.Get(fipFixture.ID, nil)
	if err != nil {
		t.Fatalf("failed to get fip: %v", err)
	}
	if fip.TargetedTo.Hostname != cpNodeFixture.server.Hostname {
		t.Fatalf("fip target: %q, want %q", fip.TargetedTo.Hostname, cpNodeFixture.server.Hostname)
	}

	// test that fip is reattached when a cp node is disabled

	// disabling the cp node fixture is tricky, since it's used for other tests,
	// so we re-target the fip to the other node.
	cherryClientFixture.IPAddresses.Update(fipFixture.ID, &cherrygo.UpdateIPAddress{TargetedTo: strconv.Itoa(cp2.server.ID)})

	err = cpNodeFixture.remove(ctx, &cp2)
	if err != nil {
		t.Fatalf("couldn't remove node from cluster: %v", err)
	}

	untilIpHasTarget(ctx, *fipFixture, fipFixture.ID)
	if err != nil {
		t.Fatalf("fip didn't get attached to cp node: %v", err)
	}

}
