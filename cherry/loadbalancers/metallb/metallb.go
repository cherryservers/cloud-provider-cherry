package metallb

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers"
	metalapi "go.universe.tf/metallb/api/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

const (
	hostnameKey         = "kubernetes.io/hostname"
	serviceNameKey      = "nomatch.cherryservers.com/service-name"
	serviceNameSpaceKey = "nomatch.cherryservers.com/service-namespace"
	defaultNamespace    = "metallb-system"
)

type Configurer interface {
	// AddPeerByService adds a peer for a specific service.
	// If a matching peer already exists with the service, do not change anything.
	// If a matching peer already exists but does not have the service, add it.
	// Returns if anything changed.
	UpdatePeersByService(ctx context.Context, adds *[]Peer, svcNamespace, svcName string) (bool, error)

	// RemovePeersByService remove peers from a particular service.
	// For any peers that have this services in the special MatchLabel, remove
	// the service from the label. If there are no services left on a peer, remove the
	// peer entirely. Not applicable with CRD
	RemovePeersByService(ctx context.Context, svcNamespace, svcName string) (bool, error)

	// AddAddressPool adds an address pool. If a matching pool already exists, do not change anything.
	// Returns if anything changed
	AddAddressPool(ctx context.Context, add *AddressPool, svcNamespace, svcName string) (bool, error)

	// RemoveAddressPool remove a pool by name. If the matching pool does not exist, do not change anything
	RemoveAddressPool(ctx context.Context, pool string) error

	// RemoveAddressPoolByAddress remove a pool by an address alone. If the matching pool does not exist, do not change anything
	RemoveAddressPoolByAddress(ctx context.Context, addr string) error

	// Read the config from the system, if it needs to be updated
	Get(context.Context) error

	// Update write the config back to the system, if necessary
	Update(context.Context) error
}

type LB struct {
	configurer Configurer
}

// NewLB returns a new LB
// The first argument used to be k8sclient. We no longer need it, as we do client.New() in NewLB.
// We keep it around in case it is needed in the future, e.g. if we need to go back to the configmap.
// In theory, we should be able to get the client we need of type "sigs.k8s.io/controller-runtime/pkg/client"
// from the k8sclient; for the future.
func NewLB(_ kubernetes.Interface, config string) *LB {
	// may have an extra slash at the beginning or end, so get rid of it
	config = strings.TrimPrefix(config, "/")
	config = strings.TrimSuffix(config, "/")
	namespace := config

	// default
	if namespace == "" {
		namespace = defaultNamespace
	}

	// get the CRD information
	scheme := runtime.NewScheme()
	_ = metalapi.AddToScheme(scheme)
	cl, err := client.New(clientconfig.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	return &LB{
		configurer: &CRDConfigurer{namespace: namespace, client: cl},
	}
}

func (l *LB) AddService(ctx context.Context, svcNamespace, svcName, ip string, nodes []loadbalancers.Node) error {
	config := l.configurer
	if err := config.Get(ctx); err != nil {
		return fmt.Errorf("unable to add retrieve metallb config: %w", err)
	}

	// Update the service and configmap/IpAddressPool and save them
	if err := mapIP(ctx, config, ip, svcNamespace, svcName); err != nil {
		return fmt.Errorf("unable to map IP to service: %w", err)
	}
	if err := l.addNodes(ctx, svcNamespace, svcName, nodes); err != nil {
		return fmt.Errorf("failed to add nodes: %w", err)
	}
	return nil
}

func (l *LB) RemoveService(ctx context.Context, svcNamespace, svcName, ip string) error {
	config := l.configurer
	if err := config.Get(ctx); err != nil {
		return fmt.Errorf("unable to retrieve metallb config: %w", err)
	}

	// remove the EIP
	if err := unmapIP(ctx, config, ip, svcNamespace, svcName); err != nil {
		return fmt.Errorf("failed to remove IP: %w", err)
	}

	// remove any node entries for this service
	// go through the peers and see if we have one with our hostname.
	removed, err := config.RemovePeersByService(ctx, svcNamespace, svcName)
	if err != nil {
		return fmt.Errorf("unable to remove service: %w", err)
	}
	if removed {
		if err := config.Update(ctx); err != nil {
			return fmt.Errorf("unable to remove service: %w", err)
		}
	}
	return nil
}

func (l *LB) UpdateService(ctx context.Context, svcNamespace, svcName string, nodes []loadbalancers.Node) error {
	// find the service whose name matches the requested svc

	// ensure nodes are correct
	if err := l.addNodes(ctx, svcNamespace, svcName, nodes); err != nil {
		return fmt.Errorf("failed to add nodes: %w", err)
	}
	return nil
}

// addNodes add one or more nodes with the provided name, srcIP, and bgp information
func (l *LB) addNodes(ctx context.Context, svcNamespace, svcName string, nodes []loadbalancers.Node) error {
	config := l.configurer
	if err := config.Get(ctx); err != nil {
		return fmt.Errorf("unable to get metallb config: %w", err)
	}

	var (
		changed       bool
		peersToUpdate []Peer
	)
	for _, node := range nodes {
		ns := []NodeSelector{
			{MatchLabels: map[string]string{
				hostnameKey: node.Name,
			}},
		}
		for i, peer := range node.Peers {
			p := Peer{
				MyASN:         uint32(node.LocalASN),
				ASN:           uint32(node.PeerASN),
				Password:      node.Password,
				Addr:          peer.Address,
				Port:          uint16(peer.Port),
				SrcAddr:       node.SourceIP,
				NodeSelectors: ns,
				Name:          fmt.Sprintf("%s-%d", node.Name, i),
			}
			peersToUpdate = append(peersToUpdate, p)
		}
	}
	// to ensure that the nodes are correct, we need to check the nodes specified
	// for these services against the whole list of nodes/peers saved in the configuration
	changed, err := config.UpdatePeersByService(ctx, &peersToUpdate, svcNamespace, svcName)
	if err != nil {
		return fmt.Errorf("unable to update nodes: %w", err)
	}
	if changed {
		return config.Update(ctx)
	}
	return nil
}

// mapIP add a given ip address to the metallb configmap
func mapIP(ctx context.Context, config Configurer, addr, svcNamespace, svcName string) error {
	klog.V(2).Infof("mapping IP %s", addr)
	return updateMapIP(ctx, config, addr, svcNamespace, svcName, true)
}

// unmapIP remove a given IP address from the metalllb config map
func unmapIP(ctx context.Context, config Configurer, addr, svcNamespace, svcName string) error {
	klog.V(2).Infof("unmapping IP %s", addr)
	return updateMapIP(ctx, config, addr, svcNamespace, svcName, false)
}

func updateMapIP(ctx context.Context, config Configurer, addr, svcNamespace, svcName string, add bool) error {
	if config == nil {
		klog.V(2).Info("config unchanged, not updating")
		return nil
	}
	name := poolName(svcNamespace, svcName)

	// update the configmap and save it
	if add {
		autoAssign := false
		added, err := config.AddAddressPool(ctx, &AddressPool{
			Protocol:   "bgp",
			Name:       name,
			Addresses:  []string{addr},
			AutoAssign: &autoAssign,
		}, svcNamespace, svcName)
		if err != nil {
			klog.V(2).ErrorS(err, "error adding IP")
			return fmt.Errorf("failed to add IP: %w", err)
		}
		if !added {
			klog.V(2).Info("address already in config, unchanged")
			return nil
		}
	} else {
		if err := config.RemoveAddressPool(ctx, name); err != nil {
			klog.V(2).Infof("error removing IPAddressPool: %v", err)
			return fmt.Errorf("error removing IPAddressPool: %w", err)
		}
	}
	klog.V(2).Info("config changed, updating")
	if err := config.Update(ctx); err != nil {
		klog.V(2).ErrorS(err, "error updating")
		return fmt.Errorf("error updating: %w", err)
	}
	return nil
}

func poolName(svcNamespace, svcName string) string {
	ns := regexp.MustCompile(`[^a-zA-Z0-9-]+`).ReplaceAllString(svcNamespace, "")
	svc := regexp.MustCompile(`[^a-zA-Z0-9-]+`).ReplaceAllString(svcName, "")
	return fmt.Sprintf("%s.%s", ns, svc)
}
