package cherry

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	cherrygo "github.com/cherryservers/cherrygo/v3"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/intstr"
	v1applyconfig "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

const (
	externalServiceName      = "cloud-provider-cherry-kubernetes-external"
	externalServiceNamespace = "kube-system"
	metallbAnnotation        = "metallb.universe.tf/address-pool"
	metallbDisabledtag       = "disabled-metallb-do-not-use-any-address-pool"
)

var controlPlaneLabels = []string{"node-role.kubernetes.io/master", "node-role.kubernetes.io/control-plane"}

/*
controlPlaneEndpointManager checks the availability of an FloatingIP for
the control plane and if it exists the reconciliation guarantees that it is
attached to a healthy control plane.

The general steps are:
1. Check if the passed FloatingIP tags returns a valid FloatingIP via Cherry Servers API.
2. If there is NOT a FloatingIP with those tags just end the reconciliation
3. If there is a FloatingIP use the kubernetes client-go to check if it
returns a valid response
4. If the response returned via client-go is good we do not need to do anything
5. If the response if wrong or it terminated it means that the device behind
the FloatingIP is not working correctly and we have to find a new one.
6. Ping the other control plane available in the cluster, if one of them work
assign the FloatingIP to that device.
7. If NO Control Planes succeed, the cluster is unhealthy and the
reconciliation terminates without changing the current state of the system.
*/
type controlPlaneEndpointManager struct {
	apiServerPort         int32 // node on which the FIP is listening
	nodeAPIServerPort     int32 // port on which the api server is listening on the control plane nodes
	fipTagKey             string
	fipTagValue           string
	cherryClient          *cherrygo.Client
	projectID             int
	httpClient            *http.Client
	k8sclient             kubernetes.Interface
	assignmentMutex       sync.Mutex
	serviceMutex          sync.Mutex
	endpointsMutex        sync.Mutex
	controlPlaneSelectors []labels.Selector
	useHostIP             bool
}

func newControlPlaneEndpointManager(k8sclient kubernetes.Interface, stop <-chan struct{}, fipTag string, projectID int, cherryClient *cherrygo.Client, apiServerPort int32, useHostIP bool) (*controlPlaneEndpointManager, error) {
	klog.V(2).Info("newControlPlaneEndpointManager()")

	if fipTag == "" {
		klog.Info("Floating IP Tag is not configured skipping control plane endpoint management.")
		return nil, nil
	}

	var fipTagKey, fipTagValue string
	parts := strings.SplitN(fipTag, "=", 2)
	if len(parts) >= 1 {
		fipTagKey = parts[0]
	}
	if len(parts) >= 2 {
		fipTagValue = parts[1]
	}
	m := &controlPlaneEndpointManager{
		httpClient: &http.Client{
			Timeout: time.Second * 5,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			}},
		fipTagKey:     fipTagKey,
		fipTagValue:   fipTagValue,
		projectID:     projectID,
		cherryClient:  cherryClient,
		apiServerPort: apiServerPort,
		k8sclient:     k8sclient,
		useHostIP:     useHostIP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stop
		cancel()
	}()

	for _, label := range controlPlaneLabels {
		req, err := labels.NewRequirement(label, selection.Exists, nil)
		if err != nil {
			return m, err
		}

		m.controlPlaneSelectors = append(m.controlPlaneSelectors, labels.NewSelector().Add(*req))
	}

	sharedInformer := informers.NewSharedInformerFactory(k8sclient, checkLoopTimerSeconds*time.Second)

	sharedInformer.Core().V1().Nodes().Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				n, _ := obj.(*v1.Node)

				// don't reconcile if api server ports are not known yet, this will be done by the services sync
				if m.apiServerPort == 0 || m.nodeAPIServerPort == 0 {
					klog.Errorf("control plane apiserver ports not provided or determined, skipping: %s", n.Name)
					return false
				}

				// only reconcile control plane nodes
				return isControlPlaneNode(n)
			},
			Handler: cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldN, newN interface{}) {
					oldNode, _ := oldN.(*v1.Node)
					newNode, _ := newN.(*v1.Node)
					klog.Infof("handling update, node: %s", newNode.Name)

					if (oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable) && newNode.Spec.Unschedulable {
						// If the node has transititioned to unschedulable
						if err := m.tryReassignAwayFromSelf(ctx, newNode); err != nil {
							klog.Errorf("failed to handle node becoming unschedulable: %v", err)
						}
					} else {
						// Attempt to do a health check
						if err := m.doHealthCheck(ctx, newNode); err != nil {
							klog.Errorf("failed to handle node health check: %v", err)
						}
					}
				},
				DeleteFunc: func(obj interface{}) {
					n, _ := obj.(*v1.Node)
					klog.Infof("handling delete, node: %s", n.Name)

					if err := m.tryReassignAwayFromSelf(ctx, n); err != nil {
						klog.Errorf("failed to handle deleted node: %v", err)
					}
				},
			},
		},
	)

	sharedInformer.Core().V1().Endpoints().Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				e, _ := obj.(*v1.Endpoints)
				if e.Namespace != metav1.NamespaceDefault && e.Name != "kubernetes" {
					return false
				}

				return true
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					k8sEndpoints, _ := obj.(*v1.Endpoints)
					if k8sEndpoints.Namespace != metav1.NamespaceDefault || k8sEndpoints.Name != "kubernetes" {
						klog.V(2).Infof("handler endpoints, ignoring %s/%s", k8sEndpoints.Namespace, k8sEndpoints.Name)
						return
					}
					klog.Infof("handling add, endpoints: %s/%s", k8sEndpoints.Namespace, k8sEndpoints.Name)

					if err := m.syncEndpoints(ctx, k8sEndpoints); err != nil {
						klog.Errorf("failed to sync endpoints from default/kubernetes to %s/%s: %v", externalServiceNamespace, externalServiceName, err)
						return
					}
				},
				UpdateFunc: func(_, obj interface{}) {
					k8sEndpoints, _ := obj.(*v1.Endpoints)
					if k8sEndpoints.Namespace != metav1.NamespaceDefault || k8sEndpoints.Name != "kubernetes" {
						klog.V(2).Infof("handler endpoints, ignoring %s/%s", k8sEndpoints.Namespace, k8sEndpoints.Name)
						return
					}
					klog.Infof("handling update, endpoints: %s/%s", k8sEndpoints.Namespace, k8sEndpoints.Name)

					if err := m.syncEndpoints(ctx, k8sEndpoints); err != nil {
						klog.Errorf("failed to sync endpoints from default/kubernetes to %s/%s: %v", externalServiceNamespace, externalServiceName, err)
						return
					}
				},
			},
		},
	)

	sharedInformer.Core().V1().Services().Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				s, _ := obj.(*v1.Service)
				if s.Namespace != metav1.NamespaceDefault && s.Name != "kubernetes" {
					return false
				}

				return true
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					k8sService, _ := obj.(*v1.Service)
					if k8sService.Namespace != metav1.NamespaceDefault || k8sService.Name != "kubernetes" {
						klog.V(2).Infof("handler services, ignoring %s/%s", k8sService.Namespace, k8sService.Name)
						return
					}
					klog.Infof("handling add, service: %s/%s", k8sService.Namespace, k8sService.Name)

					if err := m.syncService(ctx, k8sService); err != nil {
						klog.Errorf("failed to sync service from default/kubernetes to %s/%s: %v", externalServiceNamespace, externalServiceName, err)
						return
					}
				},
				UpdateFunc: func(_, obj interface{}) {
					k8sService, _ := obj.(*v1.Service)
					if k8sService.Namespace != metav1.NamespaceDefault || k8sService.Name != "kubernetes" {
						klog.V(2).Infof("handler services, ignoring %s/%s", k8sService.Namespace, k8sService.Name)
						return
					}
					klog.Infof("handling update, service: %s/%s", k8sService.Namespace, k8sService.Name)

					if err := m.syncService(ctx, k8sService); err != nil {
						klog.Errorf("failed to sync service from default/kubernetes to %s/%s: %v", externalServiceNamespace, externalServiceName, err)
						return
					}
				},
			},
		},
	)

	sharedInformer.Start(stop)
	sharedInformer.WaitForCacheSync(stop)

	return m, nil
}

func (m *controlPlaneEndpointManager) reassign(_ context.Context, nodes []*v1.Node, ip *cherrygo.IPAddress, fipURL string) error {
	klog.V(2).Info("controlPlaneEndpoint.reassign")
	// must have figured out the node port first, or nothing to do
	if m.nodeAPIServerPort == 0 {
		return errors.New("control plane node apiserver port not yet determined, cannot reassign, will try again on next loop")
	}
	for _, node := range nodes {
		// I decided to iterate over all the addresses assigned to the node to avoid network misconfiguration
		// The first one for example is the node name, and if the hostname is not well configured it will never work.
		for _, a := range node.Status.Addresses {
			if a.Type == "Hostname" {
				klog.V(2).Infof("skipping address check of type %s: %s", a.Type, a.Address)
				continue
			}
			healthCheckAddress := fmt.Sprintf("https://%s:%d/healthz", a.Address, m.nodeAPIServerPort)
			if healthCheckAddress == fipURL {
				klog.V(2).Infof("skipping address check for FIP on this node: %s", fipURL)
				continue
			}
			klog.Infof("healthcheck node %s", healthCheckAddress)
			req, err := http.NewRequest("GET", healthCheckAddress, nil)
			if err != nil {
				klog.Errorf("healthcheck failed for node %s. err \"%s\"", node.Name, err)
				continue
			}
			resp, err := m.httpClient.Do(req)

			if err != nil {
				if err != nil {
					klog.Errorf("http client error during healthcheck. err \"%s\"", err)
				}
				continue
			}

			// We have a healthy node, this is the candidate to receive the FIP
			if resp.StatusCode == http.StatusOK {
				serverID, err := serverIDFromProviderID(node.Spec.ProviderID)
				if err != nil {
					return err
				}
				if _, _, err := m.cherryClient.IPAddresses.Update(ip.ID, &cherrygo.UpdateIPAddress{TargetedTo: fmt.Sprintf("%d", serverID)}); err != nil {
					return err
				}
				klog.Infof("control plane endpoint assigned to new server %s", node.Name)
				return nil
			}
			klog.Infof("will not assign control plane endpoint to new server %s: returned http code %d", node.Name, resp.StatusCode)
		}
	}
	return errors.New("ccm didn't find a good candidate for IP allocation. Cluster is unhealthy")
}

func isControlPlaneNode(node *v1.Node) bool {
	for _, label := range controlPlaneLabels {
		if metav1.HasLabel(node.ObjectMeta, label) {
			return true
		}
	}

	return false
}

func (m *controlPlaneEndpointManager) getControlPlaneEndpointReservation() (*cherrygo.IPAddress, error) {
	ipList, _, err := m.cherryClient.IPAddresses.List(m.projectID, nil)
	if err != nil {
		return nil, err
	}

	controlPlaneEndpoint := ipReservationByAllTags(map[string]string{m.fipTagKey: m.fipTagValue}, ipList)
	if controlPlaneEndpoint == nil {
		// IP NOT FOUND nothing to do here.
		return nil, fmt.Errorf("floating IP not found. Please verify you have one with the expected tag: %s=%s", m.fipTagKey, m.fipTagValue)
	}

	return controlPlaneEndpoint, nil
}

// nodeIsAssigned determine if the cherrgo.IPAddresses is assigned to the server represented by the Kubernetes v1.Node
func (m *controlPlaneEndpointManager) nodeIsAssigned(_ context.Context, node *v1.Node, ipReservation *cherrygo.IPAddress) (bool, error) {
	for _, na := range node.Status.Addresses {
		if na.Address == ipReservation.Address {
			return true, nil
		}
	}

	return false, nil
}

func tryFilterSelf(self *v1.Node, nodes []*v1.Node) []*v1.Node {
	var remainingNodes []*v1.Node

	for i := range nodes {
		if nodes[i].Name != self.Name {
			remainingNodes = append(remainingNodes, nodes[i])
		}
	}

	if len(remainingNodes) > 0 {
		return remainingNodes
	}

	return nodes
}

func filterDeletingNodes(nodes []*v1.Node) []*v1.Node {
	var liveNodes []*v1.Node
	for i := range nodes {
		if nodes[i].DeletionTimestamp.IsZero() {
			liveNodes = append(liveNodes, nodes[i])
		}
	}

	return liveNodes
}

func tryFilterUnschedulableNodes(nodes []*v1.Node) []*v1.Node {
	var schedulableNodes []*v1.Node
	for i := range nodes {
		if nodes[i].Spec.Unschedulable {
			continue
		}

		schedulableNodes = append(schedulableNodes, nodes[i])
	}

	if len(schedulableNodes) > 0 {
		return schedulableNodes
	}

	return nodes
}

type nodeFilter func([]*v1.Node) []*v1.Node

func (m *controlPlaneEndpointManager) tryReassignAwayFromSelf(ctx context.Context, self *v1.Node) error {
	m.assignmentMutex.Lock()
	defer m.assignmentMutex.Unlock()

	controlPlaneEndpoint, err := m.getControlPlaneEndpointReservation()
	if err != nil {
		return fmt.Errorf("failed to get the control plane endpoint for the cluster: %w", err)
	}

	hasIP, err := m.nodeIsAssigned(ctx, self, controlPlaneEndpoint)
	if err != nil {
		return fmt.Errorf("failed when checking if node has the floating ip assignment: %w", err)
	}

	selfFilter := func(nodes []*v1.Node) []*v1.Node {
		return tryFilterSelf(self, nodes)
	}

	if hasIP || controlPlaneEndpoint.TargetedTo.ID == 0 {
		klog.Info("trying to reassign FIP to another node")
		return m.tryReassign(ctx, controlPlaneEndpoint, filterDeletingNodes, tryFilterUnschedulableNodes, selfFilter)
	}

	return nil
}

// tryReassign try to reassign the controlplane endpoint to a new node.
// Anything calling this function should be wrapped by a lock on m.assignmentMutex.
func (m *controlPlaneEndpointManager) tryReassign(ctx context.Context, controlPlaneEndpoint *cherrygo.IPAddress, filters ...nodeFilter) error {
	controlPlaneHealthURL := m.healthURLFromControlPlaneEndpoint(controlPlaneEndpoint)
	nodeSet := newNodeSet()

	for _, s := range m.controlPlaneSelectors {
		klog.V(5).Infof("tryReassign(): listing nodes with labelselector %s", s.String())

		nodes, err := m.k8sclient.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: s.String()})
		if err != nil {
			return fmt.Errorf("failed to list control plane nodes with labelselector %s: %w", s.String(), err)
		}

		nodeSet.addNodeList(nodes)
	}

	potentialNodes := nodeSet.filter(filters...).toList()

	if err := m.reassign(ctx, potentialNodes, controlPlaneEndpoint, controlPlaneHealthURL); err != nil {
		return fmt.Errorf("failed to assign the control plane endpoint: %w", err)
	}

	return nil
}

// healthURLFromControlPlaneEndpoint construct the URL for the controlplane endpoint healthcheck
func (m *controlPlaneEndpointManager) healthURLFromControlPlaneEndpoint(controlPlaneEndpoint *cherrygo.IPAddress) string {
	return fmt.Sprintf("https://%s:%d/healthz", controlPlaneEndpoint.Address, m.apiServerPort)
}

func (m *controlPlaneEndpointManager) syncEndpoints(ctx context.Context, k8sEndpoints *v1.Endpoints) error {
	m.endpointsMutex.Lock()
	defer m.endpointsMutex.Unlock()

	applyConfig := v1applyconfig.Endpoints(externalServiceName, externalServiceNamespace)
	for _, subset := range k8sEndpoints.Subsets {
		applyConfig = applyConfig.WithSubsets(EndpointSubsetApplyConfig(subset))
	}

	if _, err := m.k8sclient.CoreV1().Endpoints(externalServiceNamespace).Apply(
		ctx,
		applyConfig,
		metav1.ApplyOptions{FieldManager: cherryIdentifier},
	); err != nil {
		return fmt.Errorf("failed to apply endpoint %s/%s: %w", externalServiceNamespace, externalServiceName, err)
	}

	return nil
}

func (m *controlPlaneEndpointManager) syncService(ctx context.Context, k8sService *v1.Service) error {
	m.serviceMutex.Lock()
	defer m.serviceMutex.Unlock()

	// get the target port
	existingPorts := k8sService.Spec.Ports
	if len(existingPorts) < 1 {
		return errors.New("default/kubernetes service does not have any ports defined")
	}

	// track which port the kube-apiserver actually is listening on
	m.nodeAPIServerPort = existingPorts[0].TargetPort.IntVal
	// did we set a specific port, or did we request that it just be left as is?
	if m.apiServerPort == 0 {
		m.apiServerPort = m.nodeAPIServerPort
	}

	controlPlaneEndpoint, err := m.getControlPlaneEndpointReservation()
	if err != nil {
		return fmt.Errorf("failed to get the control plane endpoint for the cluster: %w", err)
	}

	// for ease of use
	fip := controlPlaneEndpoint.Address

	servicePortApplyConfig := v1applyconfig.ServicePort().
		WithName("https").
		WithPort(m.apiServerPort).
		WithProtocol("TCP").
		WithTargetPort(intstr.FromInt(int(m.nodeAPIServerPort)))

	serviceSpecApplyConfig := v1applyconfig.ServiceSpec().
		WithType(v1.ServiceTypeLoadBalancer).
		WithLoadBalancerIP(fip).
		WithPorts(servicePortApplyConfig)

	applyConfig := v1applyconfig.Service(externalServiceName, externalServiceNamespace).
		WithAnnotations(map[string]string{metallbAnnotation: metallbDisabledtag}).
		WithSpec(serviceSpecApplyConfig)

	if _, err := m.k8sclient.CoreV1().Services(externalServiceNamespace).Apply(
		ctx,
		applyConfig,
		metav1.ApplyOptions{FieldManager: cherryIdentifier},
	); err != nil {
		return fmt.Errorf("failed to apply service %s/%s: %w", externalServiceNamespace, externalServiceName, err)
	}

	statusApplyConfig := v1applyconfig.Service(externalServiceName, externalServiceNamespace).WithStatus(
		v1applyconfig.ServiceStatus().WithLoadBalancer(
			v1applyconfig.LoadBalancerStatus().WithIngress(
				v1applyconfig.LoadBalancerIngress().WithIP(fip),
			),
		),
	)

	if _, err := m.k8sclient.CoreV1().Services(externalServiceNamespace).ApplyStatus(
		ctx,
		statusApplyConfig,
		metav1.ApplyOptions{FieldManager: cherryIdentifier},
	); err != nil {
		return fmt.Errorf("failed to apply service status %s/%s: %w", externalServiceNamespace, externalServiceName, err)
	}

	return nil
}

func (m *controlPlaneEndpointManager) doHealthCheck(ctx context.Context, node *v1.Node) error {
	klog.V(5).Infof("doHealthCheck(): performing health check")

	klog.V(5).Infof("doHealthCheck(): trying to acquire assignmentMutex lock")
	m.assignmentMutex.Lock()

	defer func() {
		klog.V(5).Infof("doHealthCheck(): releasing assignmentMutex lock")
		m.assignmentMutex.Unlock()
	}()

	klog.V(5).Infof("doHealthCheck(): assignmentMutex lock acquired")

	controlPlaneEndpoint, err := m.getControlPlaneEndpointReservation()
	if err != nil {
		return fmt.Errorf("failed to get the control plane endpoint for the cluster: %w", err)
	}

	if controlPlaneEndpoint.TargetedTo.ID == 0 {
		klog.Info("doHealthCheck(): no control plane IP assignment found, trying to assign to an available controlplane node")

		return m.tryReassign(ctx, controlPlaneEndpoint, filterDeletingNodes, tryFilterUnschedulableNodes)
	}

	controlPlaneHealthURL := m.healthURLFromControlPlaneEndpoint(controlPlaneEndpoint)

	ok, err := m.nodeIsAssigned(ctx, node, controlPlaneEndpoint)
	if err != nil {
		return fmt.Errorf("failed when checking if node has the fip assignment: %w", err)
	}

	if ok {
		// Only perform the health check if the node is assigned the FIP

		if m.useHostIP {
			for _, a := range node.Status.Addresses {
				// Find the non FIP external address for the node to use for the health check
				if a.Type == v1.NodeExternalIP && a.Address != controlPlaneEndpoint.Address {
					controlPlaneHealthURL = fmt.Sprintf("https://%s:%d/healthz", a.Address, m.nodeAPIServerPort)
				}
			}
		}

		klog.Infof("doHealthCheck(): checking control plane health through ip %s", controlPlaneHealthURL)

		req, err := http.NewRequest("GET", controlPlaneHealthURL, nil)
		// we should not have an error constructing the request
		if err != nil {
			return fmt.Errorf("error constructing GET request for %s: %w", controlPlaneHealthURL, err)
		}

		resp, err := m.httpClient.Do(req)
		// if there was no error, ensure we close
		if err == nil && resp.Body != nil {
			defer resp.Body.Close()
		}

		if err != nil || resp.StatusCode != http.StatusOK {
			if err != nil {
				klog.Errorf("http client error during healthcheck, will try to reassign to a healthy node. err \"%s\"", err)
			}

			klog.Info("doHealthCheck(): health check through elastic ip failed, trying to reassign to an available controlplane node")
			return m.tryReassign(ctx, controlPlaneEndpoint, filterDeletingNodes, tryFilterUnschedulableNodes)
		}
	}

	return nil
}

type nodeSet struct {
	nodes map[string]*v1.Node
}

func newNodeSet(nodes ...*v1.Node) *nodeSet {
	ns := new(nodeSet)
	ns.nodes = make(map[string]*v1.Node, len(nodes))

	for i := range nodes {
		if nodes[i] != nil {
			ns.add(nodes[i])
		}
	}

	return ns
}

func (ns *nodeSet) addNodeList(nodes *v1.NodeList) {
	if nodes == nil {
		return
	}

	for i := range nodes.Items {
		ns.add(&nodes.Items[i])
	}
}

func (ns *nodeSet) add(node *v1.Node) {
	if node == nil {
		return
	}

	if _, ok := ns.nodes[node.Name]; !ok {
		ns.nodes[node.Name] = node
	}
}

func (ns *nodeSet) toList() []*v1.Node {
	nodes := make([]*v1.Node, 0, len(ns.nodes))

	for key := range ns.nodes {
		nodes = append(nodes, ns.nodes[key])
	}

	return nodes
}

func (ns *nodeSet) filter(filters ...nodeFilter) *nodeSet {
	nodes := ns.toList()

	for _, f := range filters {
		nodes = f(nodes)
	}

	return newNodeSet(nodes...)
}
