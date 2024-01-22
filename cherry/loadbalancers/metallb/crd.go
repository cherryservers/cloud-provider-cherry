package metallb

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metalapi "go.universe.tf/metallb/api/v1beta1"
	"golang.org/x/exp/slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultBgpAdvertisement = "equinix-metal-bgp-adv"
	cpemLabelKey            = "cloud-provider"
	cpemLabelValue          = "equinix-metal"
	svcLabelKeyPrefix       = "service-"
	svcLabelValuePrefix     = "namespace-"
)

type CRDConfigurer struct {
	namespace string // defaults to metallb-system

	client client.Client
}

var _ Configurer = (*CRDConfigurer)(nil)

func (m *CRDConfigurer) UpdatePeersByService(ctx context.Context, adds *[]Peer, svcNamespace, svcName string) (bool, error) {
	olds, err := m.listBGPPeers(ctx)
	if err != nil {
		return false, err
	}

	news := []metalapi.BGPPeer{}
	toAdd := make(map[string]metalapi.BGPPeer)
	for _, add := range *adds {
		peer := convertToBGPPeer(add, m.namespace, svcName)
		news = append(news, peer)
		toAdd[peer.Name] = peer
	}

	// if there is no Peers, add all the new ones
	if len(olds.Items) == 0 {
		for _, n := range news {
			err = m.client.Create(ctx, &n)
			if err != nil {
				return false, fmt.Errorf("unable to add BGPPeer %s: %w", n.GetName(), err)
			}
		}
		return true, nil
	}

	var changed bool
	for _, o := range olds.Items {
		found := false
		for _, n := range news {
			if n.Name == o.GetName() {
				found = true
				// remove from toAdd list
				delete(toAdd, n.Name)
				// update
				patch := client.MergeFrom(o.DeepCopy())
				var update bool
				// update services in node selectors
				if peerAddService(&o, svcNamespace, svcName) {
					update = true
				}
				// check specs
				if !peerSpecEqual(o.Spec, n.Spec) {
					o.Spec = n.Spec
					update = true
				}
				if update {
					err := m.client.Patch(ctx, &o, patch)
					if err != nil {
						return false, fmt.Errorf("unable to update IPAddressPool %s: %w", o.GetName(), err)
					}
					if !changed {
						changed = true
					}
				}
				break
			}
		}
		// if a peer in the config no longer exists for a service,
		// execute RemovePeersByService to update config
		if !found {
			updatedOrRemoved, err := m.updateOrDeletePeerByService(ctx, o, svcNamespace, svcName)
			if err != nil {
				return false, err
			}
			if !changed {
				changed = updatedOrRemoved
			}
		}
	}

	for _, n := range toAdd {
		peerAddService(&n, svcNamespace, svcName)
		err = m.client.Create(ctx, &n)
		if err != nil {
			return false, fmt.Errorf("unable to add BGPPeer %s: %w", n.GetName(), err)
		}
		changed = true
	}

	return changed, nil
}

// RemovePeersByService remove peers from a particular service.
// For any peers that have this service in the services Label, remove
// the service from the label. If there are no services left, remove the
// peer entirely.
func (m *CRDConfigurer) RemovePeersByService(ctx context.Context, svcNamespace, svcName string) (bool, error) {
	var changed bool

	olds, err := m.listBGPPeers(ctx)
	if err != nil {
		return false, err
	}

	for _, o := range olds.Items {
		removed, err := m.updateOrDeletePeerByService(ctx, o, svcNamespace, svcName)
		if err != nil {
			return false, err
		}
		if !changed {
			changed = removed
		}
	}
	return true, nil
}

// AddAddressPool adds an address pool. If a matching pool already exists, do not change anything.
// Returns if anything changed.
func (m *CRDConfigurer) AddAddressPool(ctx context.Context, add *AddressPool, svcNamespace, svcName string) (bool, error) {
	// ignore empty pool; nothing to add
	if add == nil {
		return false, nil
	}

	olds, err := m.listIPAddressPools(ctx)
	if err != nil {
		return false, fmt.Errorf("retrieve a list of IPAddressPools %s %w", m.namespace, err)
	}

	addIPAddr := convertToIPAddr(*add, m.namespace, svcNamespace, svcName)

	// go through the pools and see if we have one that matches
	// - if same service name return false
	//
	// TODO (ocobleseqx)
	// - Metallb allows ip address sharing for services, so we need to find a way to share a pool
	//   EnsureLoadBalancerDeleted filters ips by service tags, so when ip is specified and already exists
	//   it must be updted to include the new serviceNamespace/service
	for _, o := range olds.Items {
		var updateLabels, updateAddresses bool
		// if same name check services labels
		if o.GetName() == addIPAddr.GetName() {
			for k := range o.GetLabels() {
				if strings.HasPrefix(k, svcLabelKeyPrefix) {
					osvc := strings.TrimPrefix(k, svcLabelKeyPrefix)
					if osvc == svcName {
						// already exists
						return false, nil
					}
				}
			}
			// if we got here, none matched exactly, update labels
			updateLabels = true
		}
		for _, addr := range addIPAddr.Spec.Addresses {
			if slices.Contains(o.Spec.Addresses, addr) {
				updateAddresses = true
				break
			}
		}
		if updateLabels || updateAddresses {
			// update pool
			patch := client.MergeFrom(o.DeepCopy())
			if updateLabels {
				o.Labels[serviceLabelKey(svcName)] = serviceLabelValue(svcNamespace)
			}
			if updateAddresses {
				// update addreses and remove duplicates
				addresses := append(o.Spec.Addresses, addIPAddr.Spec.Addresses...)
				slices.Sort(addresses)
				o.Spec.Addresses = slices.Compact(addresses)
				o.Spec.Addresses = addresses
			}
			err := m.client.Patch(ctx, &o, patch)
			if err != nil {
				return false, fmt.Errorf("unable to update IPAddressPool %s: %w", o.GetName(), err)
			}
			return true, nil
		}
	}

	// if we got here, none matched exactly, so add it
	err = m.client.Create(ctx, &addIPAddr)
	if err != nil {
		return false, fmt.Errorf("unable to add IPAddressPool %s: %w", addIPAddr.GetName(), err)
	}

	// - if there's no BGPAdvertisement, create the default BGPAdvertisement
	// - if default BGPAdvertisement exists, update IPAddressPools
	advs, err := m.listBGPAdvertisements(ctx)
	if err != nil {
		return false, err
	}
	if len(advs.Items) == 0 {
		adv := metalapi.BGPAdvertisement{}
		adv.SetName(defaultBgpAdvertisement)
		adv.SetNamespace(m.namespace)
		adv.SetLabels(map[string]string{cpemLabelKey: cpemLabelValue})
		adv.Spec.IPAddressPools = []string{addIPAddr.Name}
		err = m.client.Create(ctx, &adv)
		if err != nil {
			return false, fmt.Errorf("unable to add default BGPAdvertisement %s: %w", adv.GetName(), err)
		}
	} else {
		for _, adv := range advs.Items {
			if adv.Name == defaultBgpAdvertisement {
				patch := client.MergeFrom(adv.DeepCopy())
				adv.Spec.IPAddressPools = append(adv.Spec.IPAddressPools, addIPAddr.Name)
				err := m.client.Patch(ctx, &adv, patch)
				if err != nil {
					return false, fmt.Errorf("unable to update BGPAdvertisement %s: %w", adv.GetName(), err)
				}
			}
		}
	}
	return true, nil
}

// RemoveAddressPool removes a pool by name. If the matching pool does not exist, do not change anything
func (m *CRDConfigurer) RemoveAddressPool(ctx context.Context, pool string) error {
	if pool == "" {
		return nil
	}

	olds, err := m.listIPAddressPools(ctx)
	if err != nil {
		return err
	}

	// go through the pools and see if we have a match
	for _, o := range olds.Items {
		if o.GetName() == pool {
			if err := m.client.Delete(ctx, &o); err != nil {
				return fmt.Errorf("unable to delete pool: %w", err)
			}
			klog.V(2).Info("addressPool removed")
		}
	}

	// TODO (ocobleseqx) if we manage bgpAdvertisements created by users
	// we will also want to check/update/remove pools specified in them
	advs, err := m.listBGPAdvertisements(ctx)
	if err != nil {
		return err
	}
	for _, adv := range advs.Items {
		if adv.Name == defaultBgpAdvertisement {
			for i, p := range adv.Spec.IPAddressPools {
				if p == pool {
					if len(adv.Spec.IPAddressPools) > 1 {
						// there are more pools, just remove pool from the default bgpAdv IPAddressPools list
						patch := client.MergeFrom(adv.DeepCopy())
						adv.Spec.IPAddressPools = slices.Delete(adv.Spec.IPAddressPools, i, i+1)
						err := m.client.Patch(ctx, &adv, patch)
						if err != nil {
							return fmt.Errorf("unable to update BGPAdvertisement %s: %w", adv.GetName(), err)
						}
					} else {
						// no pools left, delete default bgpAdv
						err = m.client.Delete(ctx, &adv)
						if err != nil {
							return fmt.Errorf("unable to delete BGPPeer %s: %w", adv.GetName(), err)
						}
					}
				}
			}
			break
		}
	}
	return nil
}

func (m *CRDConfigurer) listBGPPeers(ctx context.Context) (metalapi.BGPPeerList, error) {
	var err error
	peerList := metalapi.BGPPeerList{}
	err = m.client.List(ctx, &peerList, client.MatchingLabels{cpemLabelKey: cpemLabelValue}, client.InNamespace(m.namespace))
	if err != nil {
		err = fmt.Errorf("unable to retrieve a list of BGPPeers: %w", err)
	}
	return peerList, err
}

func (m *CRDConfigurer) listBGPAdvertisements(ctx context.Context) (metalapi.BGPAdvertisementList, error) {
	var err error
	bgpAdvList := metalapi.BGPAdvertisementList{}
	err = m.client.List(ctx, &bgpAdvList, client.MatchingLabels{cpemLabelKey: cpemLabelValue}, client.InNamespace(m.namespace))
	if err != nil {
		err = fmt.Errorf("unable to retrieve a list of BGPAdvertisements: %w", err)
	}
	return bgpAdvList, err
}

func (m *CRDConfigurer) listIPAddressPools(ctx context.Context) (metalapi.IPAddressPoolList, error) {
	var err error
	poolList := metalapi.IPAddressPoolList{}
	err = m.client.List(ctx, &poolList, client.MatchingLabels{cpemLabelKey: cpemLabelValue}, client.InNamespace(m.namespace))
	if err != nil {
		err = fmt.Errorf("unable to retrieve a list of IPAddressPools: %w", err)
	}
	return poolList, err
}

func (m *CRDConfigurer) updateOrDeletePeerByService(ctx context.Context, o metalapi.BGPPeer, svcNamespace, svcName string) (bool, error) {
	original := o.DeepCopy()
	// get the services for which this peer works
	peerChanged, size := peerRemoveService(&o, svcNamespace, svcName)

	// if service left update it, otherwise delete peer
	if peerChanged {
		if size > 0 {
			err := m.client.Patch(ctx, &o, client.MergeFrom(original))
			if err != nil {
				return false, fmt.Errorf("unable to update BGPPeer %s: %w", o.GetName(), err)
			}
		} else {
			err := m.client.Delete(ctx, &o)
			if err != nil {
				return false, fmt.Errorf("unable to delete BGPPeer %s: %w", o.GetName(), err)
			}
		}
		return true, nil
	}
	return false, nil
}

func (m *CRDConfigurer) Get(_ context.Context) error { return nil }

func (m *CRDConfigurer) Update(_ context.Context) error { return nil }

// RemoveAddressPooByAddress remove a pool by an address name. If the matching pool does not exist, do not change anything
//
//nolint:revive // ignore unused error
func (m *CRDConfigurer) RemoveAddressPoolByAddress(ctx context.Context, addrName string) error {
	return nil
}

// AddService ensures that the provided service is in the list of linked services.
func peerAddService(p *metalapi.BGPPeer, svcNamespace, svcName string) bool {
	var (
		services = []Resource{
			{Namespace: svcNamespace, Name: svcName},
		}
		selectors []metalapi.NodeSelector
	)
	for _, ns := range p.Spec.NodeSelectors {
		var namespace, name string
		for k, v := range ns.MatchLabels {
			switch k {
			case serviceNameKey:
				name = v
			case serviceNameSpaceKey:
				namespace = v
			}
		}
		// if this was not a service namespace/name selector, just add it
		if name == "" && namespace == "" {
			selectors = append(selectors, ns)
		}
		if name != "" && namespace != "" {
			// if it already had it, nothing to do, nothing change
			if svcNamespace == namespace && svcName == name {
				return false
			}
			services = append(services, Resource{Namespace: namespace, Name: name})
		}
	}
	// replace the NodeSelectors with everything except for the services
	p.Spec.NodeSelectors = selectors

	// now add the services
	sort.Sort(Resources(services))

	// if we did not find it, add it
	for _, svc := range services {
		p.Spec.NodeSelectors = append(p.Spec.NodeSelectors, metalapi.NodeSelector{
			MatchLabels: map[string]string{
				serviceNameSpaceKey: svc.Namespace,
				serviceNameKey:      svc.Name,
			},
		})
	}
	return true
}

// RemoveService removes a given service from the peer. Returns whether or not it was
// changed, and how many services are left for this peer.
func peerRemoveService(p *metalapi.BGPPeer, svcNamespace, svcName string) (bool, int) {
	var (
		found     bool
		size      int
		services  = []Resource{}
		selectors []metalapi.NodeSelector
	)
	for _, ns := range p.Spec.NodeSelectors {
		var name, namespace string
		for k, v := range ns.MatchLabels {
			switch k {
			case serviceNameKey:
				name = v
			case serviceNameSpaceKey:
				namespace = v
			}
		}
		switch {
		case name == "" && namespace == "":
			selectors = append(selectors, ns)
		case name == svcName && namespace == svcNamespace:
			found = true
		case name != "" && namespace != "" && (name != svcName || namespace != svcNamespace):
			services = append(services, Resource{Namespace: namespace, Name: name})
		}
	}
	// first put back all of the previous selectors except for the services
	p.Spec.NodeSelectors = selectors
	// then add all of the services
	sort.Sort(Resources(services))
	size = len(services)
	for _, svc := range services {
		p.Spec.NodeSelectors = append(p.Spec.NodeSelectors, metalapi.NodeSelector{
			MatchLabels: map[string]string{
				serviceNameSpaceKey: svc.Namespace,
				serviceNameKey:      svc.Name,
			},
		})
	}
	return found, size
}

func convertToIPAddr(addr AddressPool, namespace, svcNamespace, svcName string) metalapi.IPAddressPool {
	ip := metalapi.IPAddressPool{
		Spec: metalapi.IPAddressPoolSpec{
			Addresses:     addr.Addresses,
			AutoAssign:    addr.AutoAssign,
			AvoidBuggyIPs: addr.AvoidBuggyIPs,
		},
	}
	ip.SetLabels(map[string]string{
		cpemLabelKey:             cpemLabelValue,
		serviceLabelKey(svcName): serviceLabelValue(svcNamespace),
	})
	ip.SetName(addr.Name)
	ip.SetNamespace(namespace)
	return ip
}

// convertToBGPPeer converts a Peer to a BGPPeer
//
//nolint:revive // ignore unused error
func convertToBGPPeer(peer Peer, namespace, svc string) metalapi.BGPPeer {
	time, _ := time.ParseDuration(peer.HoldTime)
	bgpPeer := metalapi.BGPPeer{
		Spec: metalapi.BGPPeerSpec{
			MyASN:      peer.MyASN,
			ASN:        peer.ASN,
			Address:    peer.Addr,
			SrcAddress: peer.SrcAddr,
			Port:       peer.Port,
			HoldTime:   metav1.Duration{Duration: time},
			// KeepaliveTime: ,
			// RouterID: peer.RouterID,
			NodeSelectors: convertToNodeSelectors(peer.NodeSelectors),
			Password:      peer.Password,
			// BFDProfile:
			// EBGPMultiHop:
		},
	}
	bgpPeer.SetLabels(map[string]string{cpemLabelKey: cpemLabelValue})
	bgpPeer.SetName(peer.Name)
	bgpPeer.SetNamespace(namespace)
	return bgpPeer
}

func convertToNodeSelectors(legacy NodeSelectors) []metalapi.NodeSelector {
	nodeSelectors := make([]metalapi.NodeSelector, 0)
	for _, l := range legacy {
		nodeSelectors = append(nodeSelectors, convertToNodeSelector(l))
	}
	return nodeSelectors
}

func convertToNodeSelector(legacy NodeSelector) metalapi.NodeSelector {
	return metalapi.NodeSelector{
		MatchLabels:      legacy.MatchLabels,
		MatchExpressions: convertToMatchExpressions(legacy.MatchExpressions),
	}
}

func convertToMatchExpressions(legacy []SelectorRequirements) []metalapi.MatchExpression {
	matchExpressions := make([]metalapi.MatchExpression, 0)
	for _, l := range legacy {
		expr := metalapi.MatchExpression{
			Key:      l.Key,
			Operator: l.Operator,
			Values:   l.Values,
		}
		matchExpressions = append(matchExpressions, expr)
	}
	return matchExpressions
}

// peerSpecEqual return true if a peer is identical.
// Will only check for it in the current Peer p, and not the "other" peer in the parameter.
func peerSpecEqual(p, o metalapi.BGPPeerSpec) bool {
	// not matched if any field is mismatched except for NodeSelectors
	if p.MyASN != o.MyASN || p.ASN != o.ASN || p.Address != o.Address || p.Port != o.Port || p.HoldTime != o.HoldTime ||
		p.Password != o.Password || p.RouterID != o.RouterID {
		return false
	}
	return true
}

func serviceLabelKey(svcName string) string {
	return svcLabelKeyPrefix + svcName
}

func serviceLabelValue(svcNamespace string) string {
	return svcLabelValuePrefix + svcNamespace
}
