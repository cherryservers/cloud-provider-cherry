package cherry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/cherryservers/cherrygo"
	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers"
	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers/empty"
	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers/kubevip"
	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers/metallb"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

type loadBalancers struct {
	client              *cherrygo.Client
	k8sclient           kubernetes.Interface
	project             string
	region              string
	clusterID           string
	implementor         loadbalancers.LB
	implementorConfig   string
	annotationLocalASN  string
	annotationPeerASN   string
	annotationPeerIP    string
	annotationSrcIP     string
	fipRegionAnnotation string
	nodeSelector        labels.Selector
	bgpEnabler
}

func newLoadBalancers(client *cherrygo.Client, k8sclient kubernetes.Interface, projectID, region, config string, annotationLocalASN, annotationPeerASN, annotationPeerIP, annotationSrcIP, fipRegionAnnotation, nodeSelector string, bgpEnabler bgpEnabler) (*loadBalancers, error) {
	selector := labels.Everything()
	if nodeSelector != "" {
		selector, _ = labels.Parse(nodeSelector)
	}

	l := &loadBalancers{client, k8sclient, projectID, region, "", nil, config, annotationLocalASN, annotationPeerASN, annotationPeerIP, annotationSrcIP, fipRegionAnnotation, selector, bgpEnabler}

	// parse the implementor config and see what kind it is - allow for no config
	if l.implementorConfig == "" {
		klog.V(2).Info("loadBalancers.init(): no loadbalancer implementation config, skipping")
		return nil, nil
	}

	// get the UID of the kube-system namespace
	systemNamespace, err := k8sclient.CoreV1().Namespaces().Get(context.Background(), "kube-system", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get kube-system namespace: %w", err)
	}
	if systemNamespace == nil {
		return nil, fmt.Errorf("kube-system namespace is missing unexplainably")
	}

	u, err := url.Parse(l.implementorConfig)
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	lbconfig := u.Path
	var impl loadbalancers.LB
	switch u.Scheme {
	case "kube-vip":
		klog.Info("loadbalancer implementation enabled: kube-vip")
		impl = kubevip.NewLB(k8sclient, lbconfig)
	case "metallb":
		klog.Info("loadbalancer implementation enabled: metallb")
		impl = metallb.NewLB(k8sclient, lbconfig)
	case "empty":
		klog.Info("loadbalancer implementation enabled: empty, bgp only")
		impl = empty.NewLB(k8sclient, lbconfig)
	default:
		klog.Info("loadbalancer implementation disabled")
		impl = nil
	}

	l.clusterID = string(systemNamespace.UID)
	l.implementor = impl
	klog.V(2).Info("loadBalancers.init(): complete")
	return l, nil
}

// implementation of cloudprovider.LoadBalancer

// GetLoadBalancer returns whether the specified load balancer exists, and
// if so, what its status is.
// Implementations must treat the *v1.Service parameter as read-only and not modify it.
// Parameter 'clusterName' is the name of the cluster as presented to kube-controller-manager
func (l *loadBalancers) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (status *v1.LoadBalancerStatus, exists bool, err error) {
	svcName := serviceRep(service)
	svcTag, svcValue := serviceTag(service)
	clsTag, clsValue := clusterTag(l.clusterID)
	svcIP := service.Spec.LoadBalancerIP

	var svcIPCidr string

	// get IP address reservations and check if they any exists for this svc
	ips, _, err := l.client.IPAddresses.List(l.project)
	if err != nil {
		return nil, false, fmt.Errorf("unable to retrieve IP reservations for project %s: %w", l.project, err)
	}
	klog.V(2).Infof("got ips")
	for _, ip := range ips {
		fmt.Printf("%s %#v\n", ip.Cidr, ip.Tags)
	}

	ipReservation := ipReservationByAllTags(map[string]string{svcTag: svcValue, clsTag: clsValue, cherryTag: cherryValue}, ips)

	klog.V(2).Infof("GetLoadBalancer(): %s with existing IP assignment %s", svcName, svcIP)

	// get the IPs and see if there is anything to clean up
	if ipReservation == nil {
		klog.V(2).Infof("no reservation found, nothing to remove")
		return nil, false, nil
	}
	svcIPCidr = ipReservation.Cidr
	klog.V(2).Infof("reservation found: %s", svcIPCidr)
	return &v1.LoadBalancerStatus{
		Ingress: []v1.LoadBalancerIngress{
			{IP: svcIPCidr},
		},
	}, true, nil
}

// GetLoadBalancerName returns the name of the load balancer. Implementations must treat the
// *v1.Service parameter as read-only and not modify it.
func (l *loadBalancers) GetLoadBalancerName(ctx context.Context, clusterName string, service *v1.Service) string {
	svcTag, svcValue := serviceTag(service)
	clsTag, clsValue := clusterTag(l.clusterID)
	return fmt.Sprintf("%s=%s:%s=%s:%s=%s", cherryTag, cherryValue, svcTag, svcValue, clsTag, clsValue)
}

// EnsureLoadBalancer creates a new load balancer 'name', or updates the existing one. Returns the status of the balancer
// Implementations must treat the *v1.Service and *v1.Node
// parameters as read-only and not modify them.
// Parameter 'clusterName' is the name of the cluster as presented to kube-controller-manager
func (l *loadBalancers) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	klog.V(2).Infof("EnsureLoadBalancer(): add: service %s/%s", service.Namespace, service.Name)
	// get IP address reservations and check if they any exists for this svc
	ips, _, err := l.client.IPAddresses.List(l.project)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve IP reservations for project %s: %w", l.project, err)
	}
	ipCidr, err := l.addService(ctx, service, ips, filterNodes(nodes, l.nodeSelector))
	if err != nil {
		return nil, fmt.Errorf("failed to add service %s: %w", service.Name, err)
	}
	// get the IP only
	ip := strings.SplitN(ipCidr, "/", 2)

	return &v1.LoadBalancerStatus{
		Ingress: []v1.LoadBalancerIngress{
			{IP: ip[0]},
		},
	}, nil
}

// UpdateLoadBalancer updates hosts under the specified load balancer.
// Implementations must treat the *v1.Service and *v1.Node
// parameters as read-only and not modify them.
// Parameter 'clusterName' is the name of the cluster as presented to kube-controller-manager
func (l *loadBalancers) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) error {
	klog.V(2).Infof("UpdateLoadBalancer(): service %s", service.Name)
	// get IP address reservations and check if they any exists for this svc

	var n []loadbalancers.Node
	for _, node := range filterNodes(nodes, l.nodeSelector) {
		klog.V(2).Infof("UpdateLoadBalancer(): %s", node.Name)
		// get the node provider ID
		id := node.Spec.ProviderID
		if id == "" {
			return fmt.Errorf("no provider ID given for node %s, skipping", node.Name)
		}
		// ensure BGP is enabled for the node, and get its config
		bgpConfig, err := l.bgpEnabler.ensureNodeBGPEnabled(id)
		if err != nil {
			klog.Errorf("could not ensure BGP enabled for node %s: %v", node.Name, err)
			continue
		}
		klog.V(2).Infof("bgp enabled on node %s", node.Name)
		// ensure the node has the correct annotations
		if err := l.annotateNode(ctx, node); err != nil {
			return fmt.Errorf("failed to annotate node %s: %w", node.Name, err)
		}
		var peers []loadbalancers.Peer
		for _, p := range bgpConfig.Peers {
			peers = append(peers, loadbalancers.Peer{Address: p.Address, Port: p.Port})
		}
		n = append(n, loadbalancers.Node{
			Name:     node.Name,
			LocalASN: bgpConfig.LocalASN,
			PeerASN:  bgpConfig.RemoteASN,
			Peers:    peers,
		})
	}
	return l.implementor.UpdateService(ctx, service.Namespace, service.Name, n)
}

// EnsureLoadBalancerDeleted deletes the specified load balancer if it
// exists, returning nil if the load balancer specified either didn't exist or
// was successfully deleted.
// This construction is useful because many cloud providers' load balancers
// have multiple underlying components, meaning a Get could say that the LB
// doesn't exist even if some part of it is still laying around.
// Implementations must treat the *v1.Service parameter as read-only and not modify it.
// Parameter 'clusterName' is the name of the cluster as presented to kube-controller-manager
func (l *loadBalancers) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, service *v1.Service) error {
	// REMOVAL
	klog.V(2).Infof("EnsureLoadBalancerDeleted(): remove: %s", service.Name)
	svcName := serviceRep(service)
	svcTag, svcValue := serviceTag(service)
	clsTag, clsValue := clusterTag(l.clusterID)
	svcIP := service.Spec.LoadBalancerIP

	var svcIPCidr string

	// get IP address reservations and check if they any exists for this svc
	ips, _, err := l.client.IPAddresses.List(l.project)
	if err != nil {
		return fmt.Errorf("unable to retrieve IP reservations for project %s: %w", l.project, err)
	}

	ipReservation := ipReservationByAllTags(map[string]string{svcTag: svcValue, clsTag: clsValue, cherryTag: cherryValue}, ips)

	klog.V(2).Infof("EnsureLoadBalancerDeleted(): remove: %s with existing IP assignment %s", svcName, svcIP)

	// get the IPs and see if there is anything to clean up
	if ipReservation == nil {
		klog.V(2).Infof("EnsureLoadBalancerDeleted(): remove: no IP reservation found for %s, nothing to delete", svcName)
		return nil
	}
	// delete the reservation
	klog.V(2).Infof("EnsureLoadBalancerDeleted(): remove: for %s EIP ID %s", svcName, ipReservation.ID)
	if _, _, err := l.client.IPAddress.Remove(l.project, &cherrygo.RemoveIPAddress{ID: ipReservation.ID}); err != nil {
		return fmt.Errorf("failed to remove IP address reservation %s from project: %w", ipReservation.ID, err)
	}
	// remove it from any implementation-specific parts
	svcIPCidr = ipReservation.Cidr
	klog.V(2).Infof("EnsureLoadBalancerDeleted(): remove: for %s entry %s", svcName, svcIPCidr)
	if err := l.implementor.RemoveService(ctx, service.Namespace, service.Name, svcIPCidr); err != nil {
		return fmt.Errorf("error removing IP from configmap for %s: %w", svcName, err)
	}
	klog.V(2).Infof("EnsureLoadBalancerDeleted(): remove: removed service %s from implementation", svcName)
	return nil
}

// utility funcs

// annotateNode ensure a node has the correct annotations.
func (l *loadBalancers) annotateNode(ctx context.Context, node *v1.Node) error {
	klog.V(2).Infof("annotateNode: %s", node.Name)
	// get the node provider ID
	id, err := serverIDFromProviderID(node.Spec.ProviderID)
	if err != nil {
		return fmt.Errorf("unable to get device ID from providerID: %w", err)
	}

	// add annotations
	annotations := node.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// get the bgp info
	bgpConfig, err := l.ensureNodeBGPEnabled(id)
	switch {
	case err != nil:
		return fmt.Errorf("could not get BGP info for node %s: %w", node.Name, err)
	case len(bgpConfig.Peers) == 0:
		klog.Errorf("got BGP info for node %s but it had no peer IPs", node.Name)
	default:
		localASN := strconv.Itoa(bgpConfig.LocalASN)
		peerASN := strconv.Itoa(bgpConfig.RemoteASN)

		// we always set the peer IPs as a sorted list, so that 0, 1, n are
		// consistent in ordering
		peers := bgpConfig.Peers
		sort.Slice(peers, func(i, j int) bool {
			return peers[i].Address < peers[j].Address
		})
		var (
			i    int
			peer BGPPeer
		)

		// ensure all of the data we have is in the annotations, either
		// adding or replacing
		for i, peer = range peers {
			annotationLocalASN := strings.Replace(l.annotationLocalASN, "{{n}}", strconv.Itoa(i), 1)
			annotationPeerASN := strings.Replace(l.annotationPeerASN, "{{n}}", strconv.Itoa(i), 1)
			annotationPeerIP := strings.Replace(l.annotationPeerIP, "{{n}}", strconv.Itoa(i), 1)

			annotations[annotationLocalASN] = localASN
			annotations[annotationPeerASN] = peerASN
			annotations[annotationPeerIP] = peer.Address
		}
	}

	// TODO: ensure that any old ones that are not in the new data are removed
	// for now, since there are consistently two upstream nodes, we will not bother
	// it gets complex, because we need to match patterns. It is not worth the effort for now.

	// patch the node with the new annotations
	klog.V(2).Infof("annotateNode %s: %v", node.Name, annotations)

	mergePatch, _ := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": annotations,
		},
	})

	if _, err := l.k8sclient.CoreV1().Nodes().Patch(ctx, node.Name, k8stypes.MergePatchType, mergePatch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("failed to patch node with annotations %s: %w", node.Name, err)
	}
	klog.V(2).Infof("annotateNode %s: complete", node.Name)
	return nil
}

// addService add a single service; wraps the implementation
func (l *loadBalancers) addService(ctx context.Context, svc *v1.Service, ips []cherrygo.IPAddresses, nodes []*v1.Node) (string, error) {
	svcName := serviceRep(svc)
	svcTag, svcValue := serviceTag(svc)
	svcRegion := serviceAnnotation(svc, l.fipRegionAnnotation)
	clsTag, clsValue := clusterTag(l.clusterID)
	svcIP := svc.Spec.LoadBalancerIP

	var (
		svcIPCidr string
	)
	ipReservation := ipReservationByAllTags(map[string]string{svcTag: svcValue, clsTag: clsValue, cherryTag: cherryValue}, ips)

	klog.V(2).Infof("processing %s with existing IP assignment %s", svcName, svcIP)
	// if it already has an IP, no need to get it one
	if svcIP == "" {
		klog.V(2).Infof("no IP assigned for service %s; searching reservations", svcName)

		// if no IP found, request a new one
		if ipReservation == nil {

			// if we did not find an IP reserved, create a request
			klog.V(2).Infof("no IP assignment found for %s, requesting", svcName)
			// create a request
			// our logic as to where to create the IP:
			// 1. if region is set globally, use it; else
			// 2. if Service.Metadata.Labels["topology.kubernetes.io/region"] is set, use it; else
			// 3. Return error, cannot set an FIP
			region := l.region
			req := cherrygo.CreateIPAddress{
				Region: region,
				Tags:   &map[string]string{svcTag: svcValue, clsTag: clsValue, cherryTag: cherryValue},
			}
			switch {
			case svcRegion != "":
				req.Region = svcRegion
			case region != "":
				req.Region = region
			default:
				return "", errors.New("unable to create load balancer when no IP or region specified, either globally or on service")
			}

			ipAddr, _, err := l.client.IPAddress.Create(l.project, &req)
			if err != nil {
				return "", fmt.Errorf("failed to request an IP for the load balancer: %w", err)
			}
			ipReservation = &ipAddr
		}

		// we have an IP, either found from existing reservations or a new reservation.
		// map and assign it
		svcIP = ipReservation.Address

		// assign the IP and save it
		klog.V(2).Infof("assigning IP %s to %s", svcIP, svcName)
		intf := l.k8sclient.CoreV1().Services(svc.Namespace)
		existing, err := intf.Get(ctx, svc.Name, metav1.GetOptions{})
		if err != nil || existing == nil {
			klog.V(2).Infof("failed to get latest for service %s: %v", svcName, err)
			return "", fmt.Errorf("failed to get latest for service %s: %w", svcName, err)
		}
		existing.Spec.LoadBalancerIP = svcIP

		_, err = intf.Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			klog.V(2).Infof("failed to update service %s: %v", svcName, err)
			return "", fmt.Errorf("failed to update service %s: %w", svcName, err)
		}
		klog.V(2).Infof("successfully assigned %s update service %s", svcIP, svcName)
	}
	if ipReservation != nil {
		klog.V(2).Infof("using existing CIDR from reservation: %s", ipReservation.Cidr)
		svcIPCidr = ipReservation.Cidr
	} else {
		// our default CIDR for each address is 32
		cidr := "32"
		klog.V(2).Infof("adding CIDR %s to address %s", cidr, svcIP)
		svcIPCidr = fmt.Sprintf("%s/%s", svcIP, cidr)
	}
	klog.V(2).Infof("svcIPCidr now: %s", svcIPCidr)
	// now need to pass it the nodes

	var n []loadbalancers.Node
	for _, node := range nodes {
		// get the node provider ID
		id := node.Spec.ProviderID
		if id == "" {
			klog.Errorf("no provider ID given for node %s, skipping", node.Name)
			continue
		}
		// ensure BGP is enabled for the node
		bgpConfig, err := l.bgpEnabler.ensureNodeBGPEnabled(id)
		if err != nil {
			klog.Errorf("could not ensure BGP enabled for node %s: %v", node.Name, err)
			continue
		}
		klog.V(2).Infof("bgp enabled on node %s", node.Name)
		// ensure the node has the correct annotations
		if err := l.annotateNode(ctx, node); err != nil {
			klog.Errorf("failed to annotate node %s: %v", node.Name, err)
			continue
		}
		var peers []loadbalancers.Peer
		for _, p := range bgpConfig.Peers {
			peers = append(peers, loadbalancers.Peer{Address: p.Address, Port: p.Port})
		}
		n = append(n, loadbalancers.Node{
			Name:     node.Name,
			LocalASN: bgpConfig.LocalASN,
			PeerASN:  bgpConfig.RemoteASN,
			Peers:    peers,
		})
	}

	return svcIPCidr, l.implementor.AddService(ctx, svc.Namespace, svc.Name, svcIPCidr, n)
}

func serviceRep(svc *v1.Service) string {
	if svc == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s", svc.Namespace, svc.Name)
}

func serviceAnnotation(svc *v1.Service, annotation string) string {
	if svc == nil {
		return ""
	}
	if svc.ObjectMeta.Annotations == nil {
		return ""
	}
	return svc.ObjectMeta.Annotations[annotation]
}

func serviceTag(svc *v1.Service) (string, string) {
	if svc == nil {
		return "", ""
	}
	hash := sha256.Sum256([]byte(serviceRep(svc)))
	return "service", base64.StdEncoding.EncodeToString(hash[:])
}

func clusterTag(clusterID string) (string, string) {
	return "cluster", clusterID
}

func filterNodes(nodes []*v1.Node, nodeSelector labels.Selector) []*v1.Node {
	filteredNodes := []*v1.Node{}

	for _, node := range nodes {
		if nodeSelector.Matches(labels.Set(node.Labels)) {
			filteredNodes = append(filteredNodes, node)
		}
	}
	return filteredNodes
}
