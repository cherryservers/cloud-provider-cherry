package cherry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cherryservers/cherrygo"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

type instances struct {
	client  *cherrygo.Client
	project string
}

var (
	_ cloudprovider.InstancesV2 = (*instances)(nil)
)

func newInstances(client *cherrygo.Client, projectID string) *instances {
	return &instances{client: client, project: projectID}
}

// InstanceShutdown returns true if the node is shutdown in cloudprovider
func (i *instances) InstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	klog.V(2).Infof("called InstanceShutdown for node %s with providerID %s", node.GetName(), node.Spec.ProviderID)
	server, err := i.serverFromProviderID(node.Spec.ProviderID)
	if err != nil {
		return false, err
	}

	return server.State == "inactive", nil
}

// InstanceExists returns true if the node exists in cloudprovider
func (i *instances) InstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	klog.V(2).Infof("called InstanceExists for node %s with providerID %s", node.GetName(), node.Spec.ProviderID)
	_, err := i.serverFromProviderID(node.Spec.ProviderID)

	switch {
	case errors.Is(err, cloudprovider.InstanceNotFound):
		return false, nil
	case err != nil:
		return false, err
	}

	return true, nil
}

// InstanceMetadata returns instancemetadata for the node according to the cloudprovider
func (i *instances) InstanceMetadata(ctx context.Context, node *v1.Node) (*cloudprovider.InstanceMetadata, error) {
	server, err := i.serverByNode(node)
	if err != nil {
		return nil, err
	}
	nodeAddresses, err := nodeAddresses(*server)
	if err != nil {
		return nil, err
	}
	var p, r string
	// because plans sometimes have whitespace, we are going to replace it
	// we are also going to include the plan ID
	// from https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set
	// Valid label value:
	//
	//		must be 63 characters or less (can be empty),
	//		unless empty, must begin and end with an alphanumeric character ([a-z0-9A-Z]),
	//		could contain dashes (-), underscores (_), dots (.), and alphanumerics between.
	p = fmt.Sprintf("%d-%s", server.Plans.ID, strings.ReplaceAll(server.Plans.Name, " ", "-"))

	// "A zone represents a logical failure domain"
	// "A region represents a larger domain, made up of one or more zones"
	//
	// Cherry Servers just have regions, which matchK8s topology regions. We do not have zones for now.
	//
	// https://kubernetes.io/docs/reference/labels-annotations-taints/#topologykubernetesiozone
	r = server.Region.Name

	return &cloudprovider.InstanceMetadata{
		ProviderID:    providerIDFromServer(server),
		InstanceType:  p,
		NodeAddresses: nodeAddresses,
		Region:        r,
	}, nil
}

func nodeAddresses(server cherrygo.Server) ([]v1.NodeAddress, error) {
	var addresses []v1.NodeAddress
	addresses = append(addresses, v1.NodeAddress{Type: v1.NodeHostName, Address: server.Hostname})

	var privateIP, publicIP string
	for _, address := range server.IPAddresses {
		if address.AddressFamily == 4 {
			var addrType v1.NodeAddressType
			switch address.Type {
			case "private-ip":
				privateIP = address.Address
				addrType = v1.NodeInternalIP
			case "primary-ip", "public-ip":
				publicIP = address.Address
				addrType = v1.NodeExternalIP
			}
			addresses = append(addresses, v1.NodeAddress{Type: addrType, Address: address.Address})
		}
	}

	if privateIP == "" {
		return nil, errors.New("could not get at least one private ip")
	}

	if publicIP == "" {
		return nil, errors.New("could not get at least one public ip")
	}

	return addresses, nil
}

func (i *instances) serverByNode(node *v1.Node) (*cherrygo.Server, error) {
	if node.Spec.ProviderID != "" {
		return i.serverFromProviderID(node.Spec.ProviderID)
	}

	return serverByName(i.client, i.project, types.NodeName(node.GetName()))
}

func serverByID(client *cherrygo.Client, id string) (*cherrygo.Server, error) {
	klog.V(2).Infof("called serverByID with ID %s", id)
	server, resp, err := client.Server.List(id, nil)
	if resp.StatusCode == 404 {
		return nil, cloudprovider.InstanceNotFound
	}
	if err != nil {
		return nil, err
	}
	return &server, err
}

// serverByName returns an instance whose hostname matches the kubernetes node.Name
func serverByName(client *cherrygo.Client, projectID string, nodeName types.NodeName) (*cherrygo.Server, error) {
	klog.V(2).Infof("called serverByName with projectID %s nodeName %s", projectID, nodeName)
	if string(nodeName) == "" {
		return nil, errors.New("node name cannot be empty string")
	}
	servers, _, err := client.Servers.List(projectID, nil)
	if err != nil {
		klog.V(2).Infof("error listing servers for project %s: %v", projectID, err)
		return nil, err
	}

	for _, server := range servers {
		if server.Hostname == string(nodeName) {
			klog.V(2).Infof("Found server %s for nodeName %s", server.ID, nodeName)
			return &server, nil
		}
	}

	klog.V(2).Infof("No server found for nodeName %s", nodeName)
	return nil, cloudprovider.InstanceNotFound
}

// serverIDFromProviderID returns a server's ID from providerID.
//
// The providerID spec should be retrievable from the Kubernetes
// node object. The expected format is: cherryservers://server-id or just server-id
func serverIDFromProviderID(providerID string) (string, error) {
	klog.V(2).Infof("called serverIDFromProviderID with providerID %s", providerID)
	if providerID == "" {
		return "", errors.New("providerID cannot be empty string")
	}

	split := strings.Split(providerID, "://")
	var serverID string
	switch len(split) {
	case 2:
		serverID = split[1]
		if split[0] != ProviderName {
			return "", fmt.Errorf("provider name from providerID should be %s, was %s", ProviderName, split[0])
		}
	case 1:
		serverID = providerID
	default:
		return "", fmt.Errorf("unexpected providerID format: %s, format should be: 'server-id' or 'cherryservers://server-id'", providerID)
	}

	return serverID, nil
}

// serverFromProviderID uses providerID to get the server id and return the server
func (i *instances) serverFromProviderID(providerID string) (*cherrygo.Server, error) {
	klog.V(2).Infof("called serverFromProviderID with providerID %s", providerID)
	id, err := serverIDFromProviderID(providerID)
	if err != nil {
		return nil, err
	}

	return serverByID(i.client, id)
}

// providerIDFromServer returns a providerID from a server
func providerIDFromServer(server *cherrygo.Server) string {
	return fmt.Sprintf("%s://%d", ProviderName, server.ID)
}
