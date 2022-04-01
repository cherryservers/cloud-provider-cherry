package cherry

import (
	"fmt"

	"github.com/cherryservers/cherrygo"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const PeerPort int = 179

type BGPPeer struct {
	Address string
	Port    int
}
type NodeBGPInfo struct {
	LocalASN  int
	RemoteASN int
	Peers     []BGPPeer
}

type bgp struct {
	project   string
	client    *cherrygo.Client
	k8sclient kubernetes.Interface
	localASN  int
}

func newBGP(client *cherrygo.Client, k8sclient kubernetes.Interface, project string) (*bgp, error) {

	b := &bgp{
		client:    client,
		k8sclient: k8sclient,
		project:   project,
	}
	// enable BGP
	klog.V(2).Info("bgp.init(): enabling BGP on project")
	if err := b.enableBGP(); err != nil {
		return nil, fmt.Errorf("failed to enable BGP on project %s: %w", b.project, err)
	}
	klog.V(2).Info("bgp.init(): BGP enabled")
	return b, nil
}

// enableBGP enable bgp on the project
func (b *bgp) enableBGP() error {
	// first check if it is enabled before trying to create it
	project, _, err := b.client.Project.List(b.project)
	if err != nil {
		return fmt.Errorf("error getting project %s: %v", b.project, err)
	}
	// already configured? just return nil
	if project.Bgp.Enabled {
		b.localASN = project.Bgp.LocalASN
		return nil
	}

	// enable it
	project, _, err = b.client.Project.Update(b.project, &cherrygo.UpdateProject{
		Name: project.Name,
		Bgp:  true,
	})
	if err != nil {
		return err
	}
	b.localASN = project.Bgp.LocalASN
	return nil
}

// ensureNodeBGPEnabled check if the node has bgp enabled, and set it if it does not
func (b *bgp) ensureNodeBGPEnabled(id string) (NodeBGPInfo, error) {
	// if we are running ccm properly, then the provider ID will be on the node object
	id, err := serverIDFromProviderID(id)
	if err != nil {
		return NodeBGPInfo{}, err
	}

	// first check if it is enabled before trying to create it
	server, _, err := b.client.Server.List(id, nil)
	if err != nil {
		return NodeBGPInfo{}, fmt.Errorf("error getting server %s: %v", id, err)
	}
	// already configured? just return nil
	if server.BGP.Enabled {
		// get the BGP info on the server
		var peers []BGPPeer
		for _, p := range server.Region.BGP.Hosts {
			peers = append(peers, BGPPeer{Address: p, Port: PeerPort})
		}
		return NodeBGPInfo{LocalASN: b.localASN, RemoteASN: server.Region.BGP.Asn, Peers: peers}, nil
	}

	// enable it
	server, _, err = b.client.Server.Update(id, &cherrygo.UpdateServer{
		Tags: &server.Tags,
		Bgp:  true,
	})
	if err != nil {
		return NodeBGPInfo{}, err
	}
	// get the BGP info on the server
	var peers []BGPPeer
	for _, p := range server.Region.BGP.Hosts {
		peers = append(peers, BGPPeer{Address: p, Port: PeerPort})
	}
	return NodeBGPInfo{LocalASN: b.localASN, RemoteASN: server.Region.BGP.Asn, Peers: peers}, nil
}
