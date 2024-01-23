package cherry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/cherryservers/cherrygo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cloudprovider "k8s.io/cloud-provider"
)

var (
	projectName = "random-new-project"
)

// testNode provides a simple Node object satisfying the lookup requirements of InstanceMetadata()
func testNode(providerID, nodeName string) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Spec: v1.NodeSpec{
			ProviderID: providerID,
		},
	}
}

func TestNodeAddresses(t *testing.T) {
	vc, backend := testGetValidCloud(t, "")
	inst, _ := vc.InstancesV2()
	if inst == nil {
		t.Fatal("inst is nil")
	}
	serverName := testGetNewServerName()
	region, _ := testGetOrCreateValidRegion(validRegionName, validRegionCode, backend)
	plan, _ := testGetOrCreateValidPlan(validPlanName, backend)
	server, _ := backend.CreateServer(vc.config.ProjectID, serverName, *plan, *region)
	// update the addresses on the device; normally created by Cherry Servers as part of device provisioning
	server.IPAddresses = []cherrygo.IPAddresses{
		testCreateAddress(false, false), // private ipv4
		testCreateAddress(false, true),  // public ipv4
		testCreateAddress(true, true),   // public ipv6
	}
	err := backend.UpdateServer(server.ID, server)
	if err != nil {
		t.Fatalf("unable to update inactive device: %v", err)
	}

	validAddresses := []v1.NodeAddress{
		{Type: v1.NodeHostName, Address: serverName},
		{Type: v1.NodeInternalIP, Address: server.IPAddresses[0].Address},
		{Type: v1.NodeExternalIP, Address: server.IPAddresses[1].Address},
	}

	tests := []struct {
		testName  string
		node      *v1.Node
		addresses []v1.NodeAddress
		err       error
	}{
		{"empty node name", testNode("", ""), nil, fmt.Errorf("node name cannot be empty")},
		{"empty ID", testNode("", nodeName), nil, cloudprovider.InstanceNotFound},
		{"invalid id", testNode("cherryservers://abc123", nodeName), nil, fmt.Errorf("Error: Error response from API: invalid server ID: abc123")},
		{"unknown id", testNode(fmt.Sprintf("cherryservers://%d", randomID), nodeName), nil, cloudprovider.InstanceNotFound},
		{"valid both", testNode(fmt.Sprintf("cherryservers://%d", server.ID), serverName), validAddresses, nil},
		{"valid provider id", testNode(fmt.Sprintf("cherryservers://%d", server.ID), nodeName), validAddresses, nil},
		{"valid node name", testNode("", serverName), validAddresses, nil},
	}

	for i, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			var addresses []v1.NodeAddress

			md, err := inst.InstanceMetadata(context.TODO(), tt.node)
			if md != nil {
				addresses = md.NodeAddresses
			}
			switch {
			case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
				t.Errorf("%d: mismatched errors, actual %v expected %v", i, err, tt.err)
			case !compareAddresses(addresses, tt.addresses):
				t.Errorf("%d: mismatched addresses, actual %v expected %v", i, addresses, tt.addresses)
			}
		})
	}
}
func TestNodeAddressesByProviderID(t *testing.T) {
	vc, backend := testGetValidCloud(t, "")
	project, _ := backend.CreateProject(projectName, false)
	inst, _ := vc.InstancesV2()
	serverName := testGetNewServerName()
	region, _ := testGetOrCreateValidRegion(validRegionName, validRegionCode, backend)
	plan, _ := testGetOrCreateValidPlan(validPlanName, backend)
	server, _ := backend.CreateServer(project.ID, serverName, *plan, *region)
	// update the addresses on the device; normally created by Cherry Servers as part of provisioning
	server.IPAddresses = []cherrygo.IPAddresses{
		testCreateAddress(false, false), // private ipv4
		testCreateAddress(false, true),  // public ipv4
		testCreateAddress(true, true),   // public ipv6
	}
	err := backend.UpdateServer(server.ID, server)
	if err != nil {
		t.Fatalf("unable to update inactive device: %v", err)
	}

	validAddresses := []v1.NodeAddress{
		{Type: v1.NodeHostName, Address: serverName},
		{Type: v1.NodeInternalIP, Address: server.IPAddresses[0].Address},
		{Type: v1.NodeExternalIP, Address: server.IPAddresses[1].Address},
	}

	tests := []struct {
		testName  string
		id        string
		addresses []v1.NodeAddress
		err       error
	}{
		{"empty ID", "", nil, cloudprovider.InstanceNotFound},
		{"invalid format", fmt.Sprintf("%d", randomID), nil, cloudprovider.InstanceNotFound},
		{"not cherryservers", fmt.Sprintf("aws://%d", randomID), nil, fmt.Errorf("provider name from providerID should be cherryservers")},
		{"unknown ID", fmt.Sprintf("cherryservers://%d", randomID), nil, cloudprovider.InstanceNotFound},
		{"valid prefix", fmt.Sprintf("cherryservers://%d", server.ID), validAddresses, nil},
		{"valid without prefix", fmt.Sprintf("%d", server.ID), validAddresses, nil},
	}

	for i, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			var addresses []v1.NodeAddress

			md, err := inst.InstanceMetadata(context.TODO(), testNode(tt.id, nodeName))
			if md != nil {
				addresses = md.NodeAddresses
			}
			switch {
			case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
				t.Errorf("%d: mismatched errors, actual %v expected %v", i, err, tt.err)
			case !compareAddresses(addresses, tt.addresses):
				t.Errorf("%d: mismatched addresses, actual %v expected %v", i, addresses, tt.addresses)
			}
		})
	}
}

func TestInstanceType(t *testing.T) {
	vc, backend := testGetValidCloud(t, "")
	project, _ := backend.CreateProject(projectName, false)
	inst, _ := vc.InstancesV2()
	serverName := testGetNewServerName()
	region, _ := testGetOrCreateValidRegion(validRegionName, validRegionCode, backend)
	plan, _ := testGetOrCreateValidPlan(validPlanName, backend)
	server, _ := backend.CreateServer(project.ID, serverName, *plan, *region)
	privateIP := "10.1.1.2"
	publicIP := "25.50.75.100"
	server.IPAddresses = append(server.IPAddresses, []cherrygo.IPAddresses{
		{Address: privateIP, Type: "private-ip", AddressFamily: 4},
		{Address: publicIP, Type: "primary-ip", AddressFamily: 4},
	}...)

	tests := []struct {
		testName string
		name     string
		plan     string
		err      error
	}{
		{"empty name", "", "", cloudprovider.InstanceNotFound},
		{"invalid id", "thisdoesnotexist", "", fmt.Errorf("Error: Error response from API: invalid server ID: thisdoesnotexist")},
		{"unknown name", fmt.Sprintf("%d", randomID), "", cloudprovider.InstanceNotFound},
		{"valid", fmt.Sprintf("cherryservers://%d", server.ID), fmt.Sprintf("%d-%s", server.Plans.ID, server.Plans.Name), nil},
	}

	for i, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			var plan string
			md, err := inst.InstanceMetadata(context.TODO(), testNode(tt.name, nodeName))
			if md != nil {
				plan = md.InstanceType
			}
			switch {
			case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
				t.Errorf("%d: mismatched errors, actual %v expected %v", i, err, tt.err)
			case plan != tt.plan:
				t.Errorf("%d: mismatched id, actual %v expected %v", i, plan, tt.plan)
			}
		})
	}
}

func TestInstanceZone(t *testing.T) {
	vc, backend := testGetValidCloud(t, "")
	project, _ := backend.CreateProject(projectName, false)
	inst, _ := vc.InstancesV2()
	devName := testGetNewServerName()
	region, _ := testGetOrCreateValidRegion(validRegionName, validRegionCode, backend)
	plan, _ := testGetOrCreateValidPlan(validPlanName, backend)
	server, _ := backend.CreateServer(project.ID, devName, *plan, *region)
	privateIP := "10.1.1.2"
	publicIP := "25.50.75.100"
	server.IPAddresses = append(server.IPAddresses, []cherrygo.IPAddresses{
		{Address: privateIP, Type: "private-ip", AddressFamily: 4},
		{Address: publicIP, Type: "primary-ip", AddressFamily: 4},
	}...)

	tests := []struct {
		testName string
		name     string
		region   string
		err      error
	}{
		{"empty name", "", "", cloudprovider.InstanceNotFound},
		{"invalid id", "thisdoesnotexist", "", fmt.Errorf("Error: Error response from API: invalid server ID")},
		{"unknown name", fmt.Sprintf("%d", randomID), "", cloudprovider.InstanceNotFound},
		{"valid", fmt.Sprintf("cherryservers://%d", server.ID), server.Region.Name, nil},
	}

	for i, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			var region string
			md, err := inst.InstanceMetadata(context.TODO(), testNode(tt.name, nodeName))
			if md != nil {
				region = md.Region
			}
			switch {
			case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
				t.Errorf("%d: mismatched errors, actual %v expected %v", i, err, tt.err)
			case region != tt.region:
				t.Errorf("%d: mismatched region, actual %v expected %v", i, region, tt.region)
			}
		})
	}
}

func TestInstanceExistsByProviderID(t *testing.T) {
	vc, backend := testGetValidCloud(t, "")
	project, _ := backend.CreateProject(projectName, false)
	inst, _ := vc.InstancesV2()
	serverName := testGetNewServerName()
	region, _ := testGetOrCreateValidRegion(validRegionName, validRegionCode, backend)
	plan, _ := testGetOrCreateValidPlan(validPlanName, backend)
	server, _ := backend.CreateServer(project.ID, serverName, *plan, *region)

	tests := []struct {
		id     string
		exists bool
		err    error
	}{
		{"", false, fmt.Errorf("providerID cannot be empty")},                                                           // empty name
		{fmt.Sprintf("%d", randomID), false, nil},                                                                       // invalid format
		{fmt.Sprintf("aws://%d", randomID), false, fmt.Errorf("provider name from providerID should be cherryservers")}, // not cherryservers
		{fmt.Sprintf("cherryservers://%d", randomID), false, nil},                                                       // unknown ID
		{fmt.Sprintf("cherryservers://%d", server.ID), true, nil},                                                       // valid
		{fmt.Sprintf("%d", server.ID), true, nil},                                                                       // valid
	}

	for i, tt := range tests {
		exists, err := inst.InstanceExists(context.TODO(), testNode(tt.id, nodeName))
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("%d: mismatched errors, actual %v expected %v", i, err, tt.err)
		case exists != tt.exists:
			t.Errorf("%d: mismatched exists, actual %v expected %v", i, exists, tt.exists)
		}
	}
}

func TestInstanceShutdownByProviderID(t *testing.T) {
	vc, backend := testGetValidCloud(t, "")
	project, _ := backend.CreateProject(projectName, false)
	inst, _ := vc.InstancesV2()
	serverName := testGetNewServerName()
	region, _ := testGetOrCreateValidRegion(validRegionName, validRegionCode, backend)
	plan, _ := testGetOrCreateValidPlan(validPlanName, backend)

	serverActive, _ := backend.CreateServer(project.ID, serverName, *plan, *region)
	serverInactive, _ := backend.CreateServer(project.ID, serverName, *plan, *region)
	serverInactive.State = "inactive"
	err := backend.UpdateServer(serverInactive.ID, serverInactive)
	if err != nil {
		t.Fatalf("unable to update inactive server: %v", err)
	}

	tests := []struct {
		id   string
		down bool
		err  error
	}{
		{"", false, fmt.Errorf("providerID cannot be empty")},                                                           // empty name
		{fmt.Sprintf("%d", randomID), false, cloudprovider.InstanceNotFound},                                            // invalid format
		{fmt.Sprintf("aws://%d", randomID), false, fmt.Errorf("provider name from providerID should be cherryservers")}, // not cherryservers
		{fmt.Sprintf("cherryservers://%d", randomID), false, cloudprovider.InstanceNotFound},                            // unknown ID
		{fmt.Sprintf("cherryservers://%d", serverActive.ID), false, nil},                                                // valid
		{fmt.Sprintf("%d", serverActive.ID), false, nil},                                                                // valid
		{fmt.Sprintf("cherryservers://%d", serverInactive.ID), true, nil},                                               // valid
		{fmt.Sprintf("%d", serverInactive.ID), true, nil},                                                               // valid
	}

	for i, tt := range tests {
		down, err := inst.InstanceShutdown(context.TODO(), testNode(tt.id, nodeName))
		switch {
		case (err == nil && tt.err != nil) || (err != nil && tt.err == nil) || (err != nil && tt.err != nil && !strings.HasPrefix(err.Error(), tt.err.Error())):
			t.Errorf("%d: mismatched errors, actual %v expected %v", i, err, tt.err)
		case down != tt.down:
			t.Errorf("%d: mismatched down, actual %v expected %v", i, down, tt.down)
		}
	}
}

func compareAddresses(a1, a2 []v1.NodeAddress) bool {
	switch {
	case (a1 == nil && a2 != nil) || (a1 != nil && a2 == nil):
		return false
	case a1 == nil && a2 == nil:
		return true
	case len(a1) != len(a2):
		return false
	default:
		// sort them
		sort.SliceStable(a1, func(i, j int) bool {
			return a1[i].Type < a1[j].Type
		})
		sort.SliceStable(a2, func(i, j int) bool {
			return a2[i].Type < a2[j].Type
		})
		for i := range a1 {
			if a1[i] != a2[i] {
				return false
			}
		}
		return true
	}

}
