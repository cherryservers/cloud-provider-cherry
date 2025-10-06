package cherry

import (
	"fmt"
	"io"

	cherrygo "github.com/cherryservers/cherrygo/v3"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/component-base/version"
	"k8s.io/klog/v2"
)

const (
	ProviderName string = "cherryservers"

	// ConsumerToken token for metal consumer
	ConsumerToken string = "cloud-provider-cherry"

	// checkLoopTimerSeconds how often to resync the kubernetes informers, in seconds
	checkLoopTimerSeconds = 60
)

// cloud implements cloudprovider.Interface
type cloud struct {
	client                      *cherrygo.Client
	config                      Config
	instances                   *instances
	loadBalancer                *loadBalancers
	controlPlaneEndpointManager *controlPlaneEndpointManager
	// holds our bgp service handler
	bgp *bgp
}

type bgpEnabler interface {
	ensureNodeBGPEnabled(id string) (NodeBGPInfo, error)
}

var _ cloudprovider.Interface = (*cloud)(nil)

func newCloud(cherryConfig Config, client *cherrygo.Client) (cloudprovider.Interface, error) {
	return &cloud{
		client: client,
		config: cherryConfig,
	}, nil
}

func init() {
	cloudprovider.RegisterCloudProvider(ProviderName, func(config io.Reader) (cloudprovider.Interface, error) {
		// by the time we get here, there is no error, as it would have been handled earlier
		cherryConfig, err := getConfig(config)
		// register the provider
		if err != nil {
			return nil, fmt.Errorf("provider config error: %w", err)
		}

		// report the config
		printConfig(cherryConfig)

		// set up our client and create the cloud interface
		client, err := cherrygo.NewClient(cherrygo.WithAuthToken(cherryConfig.AuthToken))
		if err != nil {
			return nil, fmt.Errorf("unable to initialize cherrygo client: %v", err)
		}
		client.UserAgent = fmt.Sprintf("cloud-provider-cherry/%s", version.Get())
		cloud, err := newCloud(cherryConfig, client)
		if err != nil {
			return nil, fmt.Errorf("failed to create new cloud handler: %w", err)
		}
		// note that this is not fully initialized until it calls cloud.Initialize()

		return cloud, nil
	})
}

// Initialize provides the cloud with a kubernetes client builder and may spawn goroutines
// to perform housekeeping activities within the cloud provider.
func (c *cloud) Initialize(clientBuilder cloudprovider.ControllerClientBuilder, stop <-chan struct{}) {
	klog.V(5).Info("called Initialize")
	clientset := clientBuilder.ClientOrDie("cloud-provider-cherry-shared-informers")
	restCfg := clientBuilder.ConfigOrDie("cloud-provider-cherry-health-checker")

	// initialize the individual services
	projectID := c.config.ProjectID
	epm, err := newControlPlaneEndpointManager(clientset, restCfg, stop, c.config.FIPTag, projectID, c.client, c.config.APIServerPort, c.config.FIPHealthCheckUseHostIP)
	if err != nil {
		klog.Fatalf("could not initialize ControlPlaneEndpointManager: %v", err)
	}
	bgp, err := newBGP(c.client, clientset, projectID)
	if err != nil {
		klog.Fatalf("could not initialize BGP: %v", err)
	}
	lb, err := newLoadBalancers(c.client, clientset, projectID, c.config.Region, c.config.LoadBalancerSetting, c.config.AnnotationLocalASN, c.config.AnnotationPeerASN, c.config.AnnotationPeerIP, c.config.AnnotationSrcIP, c.config.AnnotationFIPRegion, c.config.BGPNodeSelector, bgp)
	if err != nil {
		klog.Fatalf("could not initialize LoadBalancers: %v", err)
	}

	c.loadBalancer = lb
	c.bgp = bgp
	c.instances = newInstances(c.client, projectID)
	c.controlPlaneEndpointManager = epm

	klog.Info("Initialize of cloud provider complete")
}

// LoadBalancer returns a balancer interface. Also returns true if the interface is supported, false otherwise.
func (c *cloud) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	klog.V(5).Info("called LoadBalancer")
	return c.loadBalancer, c.loadBalancer != nil
}

// Instances returns an instances interface. Also returns true if the interface is supported, false otherwise.
func (c *cloud) Instances() (cloudprovider.Instances, bool) {
	klog.V(5).Info("called Instances")
	return nil, false
}

// InstancesV2 returns an implementation of cloudprovider.InstancesV2.
func (c *cloud) InstancesV2() (cloudprovider.InstancesV2, bool) {
	klog.V(5).Info("called InstancesV2")
	return c.instances, true
}

// Zones returns a zones interface. Also returns true if the interface is supported, false otherwise.
// DEPRECATED. Will not be called if InstancesV2 is implemented
func (c *cloud) Zones() (cloudprovider.Zones, bool) {
	klog.V(5).Info("called Zones")
	return nil, false
}

// Clusters returns a clusters interface.  Also returns true if the interface is supported, false otherwise.
func (c *cloud) Clusters() (cloudprovider.Clusters, bool) {
	klog.V(5).Info("called Clusters")
	return nil, false
}

// Routes returns a routes interface along with whether the interface is supported.
func (c *cloud) Routes() (cloudprovider.Routes, bool) {
	klog.V(5).Info("called Routes")
	return nil, false
}

// ProviderName returns the cloud provider ID.
func (c *cloud) ProviderName() string {
	klog.V(2).Infof("called ProviderName, returning %s", ProviderName)
	return ProviderName
}

// HasClusterID returns true if a ClusterID is required and set
func (c *cloud) HasClusterID() bool {
	klog.V(5).Info("called HasClusterID")
	return true
}
