package metallb

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	hostnameKey = "kubernetes.io/hostname"
	regionKey   = "topology.kubernetes.io/region"
	holdTime    = "30s"
)

// BGPPeerMode defines the available BGP peering modes for the CCM.
type BGPPeerMode int

func (m BGPPeerMode) String() string {
	switch m {
	case BGPPeerModeNone:
		return "none"
	case BGPPeerModeNative:
		return "native"
	case BGPPeerModeFRR:
		return "frr"
	default:
		return "unknown"
	}
}

const (
	// BGPPeerModeNone disables CCM managed BGPPeers entirely.
	BGPPeerModeNone BGPPeerMode = iota

	// BGPPeerModeNative is the native-compatible BGP peering mode.
	BGPPeerModeNative

	// BGPPeerModeFRR is the FRR-compatible BGP peering mode.
	BGPPeerModeFRR
)

// ParseConfigURL parses the namespace and BGP peering mode
// from a metallb configuration URL. If not configured,
// native BGP peering mode will be used.
func ParseConfigURL(config *url.URL) (
	namespace string, mode BGPPeerMode, err error) {
	namespace = config.Path

	// May have an extra slash at the beginning or end, so get rid of it.
	namespace = strings.TrimPrefix(namespace, "/")
	namespace = strings.TrimSuffix(namespace, "/")

	rawMode := config.Query().Get("bgp-peer-mode")
	switch rawMode {
	case "native":
		mode = BGPPeerModeNative
	case "frr":
		mode = BGPPeerModeFRR
	case "none":
		mode = BGPPeerModeNone
	case "":
		mode = BGPPeerModeNative
	default:
		return namespace, BGPPeerModeNative,
			fmt.Errorf("invalid 'bgp-peer-mode' parameter: %q", rawMode)
	}

	return namespace, mode, nil
}

// NewPeersFromNodesFunc builds the appropriate PeersFromNodesFunc, based on
// the BGPPeerMode. `nodeSelectors` will be merged with the BGPPeer's NodeSelectors,
// when in FRR mode. This is not required for native mode, since each node gets
// separate BGPPeers.
func NewPeersFromNodesFunc(mode BGPPeerMode, nodeSelectors metav1.LabelSelector,
) PeersFromNodesFunc {
	switch mode {
	case BGPPeerModeFRR:
		return NewPeersFromNodesFRRFunc(nodeSelectors)
	case BGPPeerModeNative:
		return PeersFromNodesNative
	case BGPPeerModeNone:
		return PeersFromNodesNone
	default:
		return PeersFromNodesNative
	}
}

// PeersFromNodesFunc builds a list of peers for the provided nodes.
type PeersFromNodesFunc func(nodes []loadbalancers.Node) []Peer

// PeersFromNodesNone always returns a nil slice.
func PeersFromNodesNone(_ []loadbalancers.Node) []Peer {
	return nil
}

// PeersFromNodesNative builds a list of peers for the provided nodes.
// Each node gets a distinct peer object for each of it's possible peers.
// This makes it incompatible with metallb's FRR mode, since BGPPeers
// with duplicate destination IPs are not allowed.
func PeersFromNodesNative(nodes []loadbalancers.Node) []Peer {
	var peers []Peer
	for _, node := range nodes {
		ns := []NodeSelector{{MatchLabels: map[string]string{
			hostnameKey: node.Name,
		}}}
		for i, peer := range node.Peers {
			p := Peer{
				MyASN:         uint32(node.LocalASN),
				ASN:           uint32(node.PeerASN),
				Password:      node.Password,
				Addr:          peer.Address,
				Port:          uint16(peer.Port),
				SrcAddr:       node.SourceIP,
				HoldTime:      holdTime,
				NodeSelectors: ns,
				Name:          fmt.Sprintf("%s-%d", node.Name, i),
			}
			peers = append(peers, p)
		}
	}
	return peers
}

// Unfortunately, `convertToNodeSelector` already exists.
func convertToMetalLBNodeSelector(ls metav1.LabelSelector) NodeSelector {
	selectorReqs := make([]SelectorRequirements, len(ls.MatchExpressions))
	for i, req := range ls.MatchExpressions {
		selectorReqs[i] = SelectorRequirements{
			Key:      req.Key,
			Operator: string(req.Operator),
			Values:   req.Values,
		}
	}
	ns := NodeSelector{MatchLabels: ls.MatchLabels, MatchExpressions: selectorReqs}
	return ns
}

// NewPeersFromNodesFFRFunc builds a FRR mode-compatible PeersFromNodesFunc.
//
// In contrast to PeersFromNodesNative, the peer slice built from this function
// will not have duplicate peers. Instead, peers will have node selectors based on the
// node region label, merged with the provided `nodeSelector`.
//
// Also, ebgpMultiHop will be enabled for every peer, as this is required in FRR mode.
func NewPeersFromNodesFRRFunc(nodeSelector metav1.LabelSelector) PeersFromNodesFunc {
	ns := convertToMetalLBNodeSelector(nodeSelector)
	return func(nodes []loadbalancers.Node) []Peer {
		peers := make(map[string]Peer)
		for _, node := range nodes {
			nsDup := ns.Duplicate()
			nsDup.MatchLabels[regionKey] = node.Region
			for i, peer := range node.Peers {
				p := Peer{
					MyASN:         uint32(node.LocalASN),
					ASN:           uint32(node.PeerASN),
					Addr:          peer.Address,
					Port:          uint16(peer.Port),
					HoldTime:      holdTime,
					EBGPMultiHop:  true,
					NodeSelectors: []NodeSelector{nsDup},
					// Region slugs start with an upper case country code,
					// so a conversion to lower case is needed.
					Name: strings.ToLower(fmt.Sprintf("%s-%d", node.Region, i)),
				}
				peers[p.Name] = p
			}
		}
		return slices.SortedFunc(maps.Values(peers), func(a, b Peer) int {
			if a.Name < b.Name {
				return -1
			} else if a.Name > b.Name {
				return 1
			}
			return 0
		})
	}
}
