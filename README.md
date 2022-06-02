# Kubernetes Cloud Controller Manager for Cherry Servers

[![GitHub release](https://img.shields.io/github/release/cherryservers/cloud-provider-cherry/all.svg?style=flat-square)](https://github.com/cherryservers/cloud-provider-cherry/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/cherryservers/cloud-provider-cherry)](https://goreportcard.com/report/github.com/cherryservers/cloud-provider-cherry)
![Continuous Integration](https://github.com/cherryservers/cloud-provider-cherry/workflows/Continuous%20Integration/badge.svg)
[![Docker Pulls](https://img.shields.io/docker/pulls/cherryservers/cloud-provider-cherry.svg)](https://hub.docker.com/r/cherryservers/cloud-provider-cherry/)
![Cherry Servers Maintained](https://img.shields.io/badge/stability-maintained-green.svg)


`cloud-provider-cherry` is the Kubernetes CCM implementation for Cherry Servers. Read more about the CCM in [the official Kubernetes documentation](https://kubernetes.io/docs/tasks/administer-cluster/running-cloud-controller/).

This repository is **Maintained**!

## Requirements

At the current state of Kubernetes, running the CCM requires a few things.
Please read through the requirements carefully as they are critical to running the CCM on a Kubernetes cluster.

### Version

Recommended versions of Cherry Servers CCM based on your Kubernetes version:

* Cherry Servers CCM version v1.0.0+ supports Kubernetes version >=1.20.0

## Deployment

**TL;DR**

1. Set Kubernetes binary arguments correctly
1. Get your Cherry Servers project ID and secret API token
1. Deploy your Cherry Servers project ID and secret API token to your cluster in a [secret](https://kubernetes.io/docs/concepts/configuration/secret/)
1. Deploy the CCM
1. Deploy the load balancer (optional)

### Kubernetes Binary Arguments

Control plane binaries in your cluster must start with the correct flags:

* `kubelet`: All kubelets in your cluster **MUST** set the flag `--cloud-provider=external`. This must be done for _every_ kubelet. Note that [k3s](https://k3s.io) sets its own CCM by default. If you want to use the CCM with k3s, you must disable the k3s CCM and enable this one, as `--disable-cloud-controller --kubelet-arg cloud-provider=external`.
* `kube-apiserver` and `kube-controller-manager` must **NOT** set the flag `--cloud-provider`. They then will use no cloud provider natively, leaving room for the Cherry Servers CCM.

**WARNING**: setting the kubelet flag `--cloud-provider=external` will taint all nodes in a cluster with `node.cloudprovider.kubernetes.io/uninitialized`.
The CCM itself will untaint those nodes when it initializes them.
Any pod that does not tolerate that taint will be unscheduled until the CCM is running.

You **must** set the kubelet flag the first time you run the kubelet. Stopping the kubelet, adding it after,
and then restarting it will not work.

#### Kubernetes node names must match the device name

By default, the kubelet will name nodes based on the node's hostname.
Cherry Servers device hostnames are set based on the name of the device.
It is important that the Kubernetes node name matches the device name.

### Get Cherry Servers Project ID and API Token

To run `cloud-provider-cherry`, you need your Cherry Servers project ID and secret API key ID that your cluster is running in.
If you are already logged into the [Cherry Servers portal](https://portal.cherryservers.com/), you can create one by clicking on your
profile in the upper right then "API keys".
To get your project ID, select your team from the upper left corner, and then `View All Projects` from the pull-down.

You will see a list of projects. Next to the name of the project that you want to use, in `()` brackets, you will
see the project ID.

Alternatively, you can click into the project. Now check the URL, and it will give you your project ID. For example:

```
https://portal.cherryservers.com/#/projects/87553/servers
```

In this case, the project is `87553`.

Once you have this information you will be able to fill in the config needed for the CCM.

### Deploy Project and API

Copy [deploy/template/secret.yaml](./deploy/template/secret.yaml) to someplace useful:

```bash
cp deploy/template/secret.yaml /tmp/secret.yaml
```

Replace the placeholder in the copy with your token. When you're done, the `yaml` should look something like this:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cherry-cloud-config
  namespace: kube-system
stringData:
  cloud-sa.json: |
    {
    "apiKey": "abc123abc123abc123",
    "projectID": 0123456789
    }  
```


Then apply the secret, e.g.:

```bash
kubectl apply -f /tmp/secret.yaml
```

You can confirm that the secret was created with the following:

```bash
$ kubectl -n kube-system get secrets cherry-cloud-config
NAME                  TYPE                                  DATA      AGE
cherry-cloud-config   Opaque                                1         2m
```

### Deploy CCM

To apply the CCM itself, select your release and apply the manifest:

```
RELEASE=v2.0.0
kubectl apply -f https://github.com/cherryservers/cloud-provider-cherry/releases/download/${RELEASE}/deployment.yaml
```

The CCM uses multiple configuration options. See the [configuration](#Configuration) section for all of the options.

#### Deploy Load Balancer

If you want load balancing to work as well, deploy a supported load-balancer.

CCM provides the correct logic, if necessary, to manage load balancer configs for supported load-balancers.

See further in this document under loadbalancing, for details.

### Logging

By default, ccm does minimal logging, relying on the supporting infrastructure from kubernetes. However, it does support
optional additional logging levels via the `--v=<level>` flag. In general:

* `--v=2`: log most function calls for devices and facilities, when relevant logging the returned values
* `--v=3`: log additional data when logging returned values, usually entire go structs
* `--v=5`: log every function call, including those called very frequently

## Configuration

The Cherry Servers CCM has multiple configuration options. These include several different ways to set most of them, for your convenience.

1. Command-line flags, e.g. `--option value` or `--option=value`; if not set, then
1. Environment variables, e.g. `CCM_OPTION=value`; if not set, then
1. Field in the configuration [secret](https://kubernetes.io/docs/concepts/configuration/secret/); if not set, then
1. Default, if available; if not available, then an error

This section lists each configuration option, and whether it can be set by each method.

| Purpose | CLI Flag | Env Var | Secret Field | Default |
| --- | --- | --- | --- | --- |
| Path to config secret |    |    | `cloud-config` | error |
| API Key |    | `CHERRY_API_KEY` | `apiKey` | error |
| Project ID |    | `CHERRY_PROJECT_ID` | `projectID` | error |
| Region in which to create LoadBalancer Floating IPs |    | `CHERRY_REGION_NAME` | `region` | Service-specific annotation, else error |
| Base URL to Cherry Servers API |    |    | `base-url` | Official Cherry Servers API |
| Load balancer setting |   | `CHERRY_LOAD_BALANCER` | `loadbalancer` | none |
| Kubernetes annotation to set node's BGP ASN, `{{n}}` replaced with ordinal index of peer |   | `CHERRY_ANNOTATION_LOCAL_ASN` | `annotationLocalASN` | `"cherryservers.com/bgp-peers-{{n}}-node-asn"` |
| Kubernetes annotation to set BGP peer's ASN, {{n}} replaced with ordinal index of peer |   | `CHERRY_ANNOTATION_PEER_ASN` | `annotationPeerASN` | `"cherryservers.com/bgp-peers-{{n}}-peer-asn"` |
| Kubernetes annotation to set BGP peer's IPs, {{n}} replaced with ordinal index of peer |   | `CHERRY_ANNOTATION_PEER_IP` | `annotationPeerIP` | `"cherryservers.com/bgp-peers-{{n}}-peer-ip"` |
| Kubernetes annotation to set source IP for BGP peering, {{n}} replaced with ordinal index of peer |   | `CHERRY_ANNOTATION_SRC_IP` | `annotationSrcIP` | `"cherryservers.com/bgp-peers-{{n}}-src-ip"` |
| Kubernetes annotation to set the CIDR for the network range of the private address |  | `CHERRY_ANNOTATION_NETWORK_IPV4_PRIVATE` |  `annotationNetworkIPv4Private` | `cherryservers.com/network-4-private` |
| Kubernetes Service annotation to set Floating IP region |   | `CHERRY_ANNOTATION_FIP_REGION` | `annotationFIPRegion` | `"cherryservers.com/fip-region"` |
| Tag for control plane Floating IP, in `"key=value"` format |    | `CHERRY_FIP_TAG` | `fipTag` | No control plane Floating IP |
| Kubernetes API server port for Floating IP |     | `CHERRY_API_SERVER_PORT` | `apiServerPort` | Same as `kube-apiserver` on control plane nodes, same as `0` |
| Filter for cluster nodes on which to enable BGP |    | `CHERRY_BGP_NODE_SELECTOR` | `bgpNodeSelector` | All nodes |
| Use host IP for Control Plane endpoint health checks | | `CHERRY_FIP_HEALTH_CHECK_USE_HOST_IP` | `fipHealthCheckUseHostIP` | false |

**Region Note:** In all cases, where a "region" is required, use the _full name_ of the region. For example,
the region `"EU-Nord-1"` is also known by its ISO 2-character designation `"LT"`. All usages of region should
use `"EU-Nord-1"` unless otherwise specified.

## How It Works

The Kubernetes CCM for Cherry Servers deploys as a `Deployment` into your cluster with a replica of `1`. It provides the following services:

* lists and retrieves instances by ID, returning Cherry Servers instances
* manages load balancers

### Load Balancers

Cherry Servers does not offer managed load balancers like [AWS ELB](https://aws.amazon.com/elasticloadbalancing/)
or [GCP Load Balancers](https://cloud.google.com/load-balancing/). Instead, if configured to do so,
Cherry Servers CCM will interface with and configure external bare-metal loadbalancers.

When a load balancer is enabled, the CCM does the following:

1. Enable BGP for the project
1. Enable BGP on each node as it comes up
1. For each `Service` of `type=LoadBalancer`:
   * If you have specified a load balancer IP on `Service.Spec.LoadBalancerIP` (bring your own IP, or BYOIP), do nothing
   * If you have not specified a load balancer IP on `Service.Spec.LoadBalancerIP`, get a Cherry Servers Floating IP and set it on `Service.Spec.LoadBalancerIP`, see below
1. Pass control to the specific load balancer implementation

#### Service Load Balancer IP

There are two options for getting a Floating IP (FIP) for a Service of `type=LoadBalancer`: bring-your-own
or let CCM create one using the Cherry Servers API.

Whether you bring your own IP or rely on CCM to request one for you, the load balancer IP will be set, and
load balancers can consume them.

##### Bring Your Own IP

Whenever a `Service` of `type=LoadBalancer` is encountered, the CCM tries to ensure that an externally accessible load balancer IP is available.
It does this in one of two ways:

If you want to use a specific IP that you have ready, either because you brought it from the outside or because you retrieved an Floating IP
from Cherry Servers separately, you can add it to the `Service` explicitly as `Service.Spec.LoadBalancerIP`. For example:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: ip-service
spec:
  selector:
    app: MyAppIP
  ports:
    - protocol: TCP
      port: 80
      targetPort: 9376
  type: LoadBalancer
  loadBalancerIP: 145.60.80.60
```

CCM will detect that `loadBalancerIP` already was set and not try to create a new Cherry Servers Floating IP.

##### Cherry Servers FIP

If the `Service.Spec.LoadBalancerIP` was *not* set, then CCM will use the Cherry Servers API to request a new,
region-specific Floating IP and set it to `Service.Spec.LoadBalancerIP`.

The CCM needs to determine where to request the FIP. It does not attempt to figure out where the nodes are, as that can change over time,
the nodes might not be in existence when the CCM is running or `Service` is created, and you could run a Kubernetes cluster across
multiple regions, or even cloud providers.

The CCM uses the following rules to determine where to create the FIP:

1. if region is set globally using the environment variable `CHERRY_REGION_NAME`, use it; else
1. if the `Service` for which the FIP is being created has the annotation indicating in which region the FIP should be created, use it; else
1. Return an error, cannot set a FIP

The overrides of environment variable and config file are provided so that you can run explicitly control where the FIPs
are created at a system-wide level, ignoring the annotations.

Using these flags and annotations, you can run the CCM on a node in a different region, or even outside of Cherry Servers entirely.

#### Control Plane LoadBalancer Implementation

For the control plane nodes, the Cherry Servers CCM uses static Floating IP assignment, via the Cherry Servers API, to tell the
Cherry Servers network which control plane node should receive the traffic. For more details on the control plane
load-balancer, see [this section](#control-plane-load-balancing).

#### Service LoadBalancer Implementations

Loadbalancing is enabled as follows.

1. If the environment variable `CHERRY_LOAD_BALANCER` is set, read that. Else...
1. If the config file has a key named `loadbalancer`, read that. Else...
1. Load balancing is disabled.

The value of the loadbalancing configuration is `<type>:///<detail>` where:

* `<type>` is the named supported type, of one of those listed below
* `<detail>` is any additional detail needed to configure the implementation, details in the description below

For loadbalancing for Kubernetes `Service` of `type=LoadBalancer`, the following implementations are supported:

* [kube-vip](#kube-vip)
* [MetalLB](#metallb)
* [empty](#empty)

CCM does **not** deploy _any_ load balancers for you. It limits itself to managing the Cherry Servers-specific
API calls to support a load balancer, and providing configuration for supported load balancers.

##### kube-vip

When the [kube-vip](https://kube-vip.io) option is enabled, for user-deployed Kubernetes `Service` of `type=LoadBalancer`,
the Cherry Servers CCM enables BGP on the project and nodes, assigns a FIP for each such
`Service`, and adds annotations to the nodes. These annotations are configured to be consumable
by kube-vip.

To enable it, set the configuration `CHERRY_LOAD_BALANCER` or config `loadbalancer` to:

```
kube-vip://
```

Directions on using configuring kube-vip in this method are available at the kube-vip [site](https://kube-vip.io/hybrid/daemonset/)

If `kube-vip` management is enabled, then CCM does the following.

1. Enable BGP on the Cherry Servers project
1. For each node currently in the cluster or added:
   * retrieve the node's Cherry Servers ID via the node provider ID
   * retrieve the device's BGP configuration: node ASN, peer ASN, peer IPs, source IP
   * add the information to appropriate annotations on the node
1. For each service of `type=LoadBalancer` currently in the cluster or added:
   * if a Floating IP address reservation with the appropriate tags exists, and the `Service` already has that IP address affiliated with it, it is ready; ignore
   * if a Floating IP address reservation with the appropriate tags exists, and the `Service` does not have that IP affiliated with it, add it to the [service spec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.17/#servicespec-v1-core)
   * if a Floating IP address reservation with the appropriate tags does not exist, create it and add it to the services spec
1. For each service of `type=LoadBalancer` deleted from the cluster:
   * find the Floating IP address from the service spec and remove it
   * delete the Floating IP reservation from Cherry Servers

##### MetalLB

**Minimum Version**: MetalLB [version 0.11.0](https://metallb.universe.tf/release-notes/#version-0-11-0)

When [MetalLB](https://metallb.universe.tf) is enabled, for user-deployed Kubernetes `Service` of `type=LoadBalancer`,
the Cherry Servers CCM uses BGP and to provide the _equivalence_ of load balancing, without
requiring an additional managed service (or hop). BGP route advertisements enable Cherry Servers's network
to route traffic for your services at the Floating IP to the correct host.

To enable it, set the configuration `CHERRY_LOAD_BALANCER` or config `loadbalancer` to:

```
metallb:///<configMapNamespace>/<configMapName>
```

For example:

* `metallb:///metallb-system/config` - enable `MetalLB` management and update the configmap `config` in the namespace `metallb-system`
* `metallb:///foonamespace/myconfig` -  - enable `MetalLB` management and update the configmap `myconfig` in the namespace `foonamespae`
* `metallb:///` - enable `MetalLB` management and update the default configmap, i.e. `config` in the namespace `metallb-system`

Notice the **three* slashes. In the URL, the namespace and the configmap are in the path.

When enabled, CCM controls the loadbalancer by updating the provided `ConfigMap`.

If `MetalLB` management is enabled, then CCM does the following.

1. Get the appropriate namespace and name of the `ConfigMap`, based on the rules above.
1. If the `ConfigMap` does not exist, do the rest of the behaviours, but do not update the `ConfigMap`
1. Enable BGP on the Cherry Servers project
1. For each node currently in the cluster or added:
   * retrieve the node's Cherry Server ID via the node provider ID
   * retrieve the device's BGP configuration: node ASN, peer ASN, peer IPs, source IP
   * add them to the metallb `ConfigMap` with a kubernetes selector ensuring that the peer is only for this node
1. For each node deleted from the cluster:
   * remove the node from the MetalLB `ConfigMap`
1. For each service of `type=LoadBalancer` currently in the cluster or added:
   * if a Floating IP address reservation with the appropriate tags exists, and the `Service` already has that IP address affiliated with it, it is ready; ignore
   * if a Floating IP address reservation with the appropriate tags exists, and the `Service` does not have that IP affiliated with it, add it to the [service spec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.17/#servicespec-v1-core) and ensure it is in the pools of the MetalLB `ConfigMap` with `auto-assign: false`
   * if a Floating IP address reservation with the appropriate tags does not exist, create it and add it to the services spec, and ensure is in the pools of the metallb `ConfigMap` with `auto-assign: false`
1. For each service of `type=LoadBalancer` deleted from the cluster:
   * find the Floating IP address from the service spec and remove it
   * remove the IP from the `ConfigMap`
   * delete the Floating IP reservation from Cherry Servers

CCM itself does **not** deploy the load-balancer or any part of it, including the `ConfigMap`. It only
modifies an existing `ConfigMap`. This can be deployed by the administrator separately, using the manifest
provided in the releases page, or in any other manner.

##### empty

When the `empty` option is enabled, for user-deployed Kubernetes `Service` of `type=LoadBalancer`,
the Cherry Servers CCM enables BGP on the project and nodes, assigns a FIP for each such
`Service`, and adds annotations to the nodes. It does not integrate directly with any load balancer.
This is useful if you have your own implementation, but want to leverage Cherry Servers CCM's
management of BGP and FIPs.

To enable it, set the configuration `CHERRY_LOAD_BALANCER` or config `loadbalancer` to:

```
empty://
```

If `empty` management is enabled, then CCM does the following.

1. Enable BGP on the Cherry Servers project
1. For each node currently in the cluster or added:
   * retrieve the node's Cherry Servers ID via the node provider ID
   * retrieve the device's BGP configuration: node ASN, peer ASN, peer IPs, source IP
   * add the information to appropriate annotations on the node
1. For each service of `type=LoadBalancer` currently in the cluster or added:
   * if a Floating IP address reservation with the appropriate tags exists, and the `Service` already has that IP address affiliated with it, it is ready; ignore
   * if a Floating IP address reservation with the appropriate tags exists, and the `Service` does not have that IP affiliated with it, add it to the [service spec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.17/#servicespec-v1-core)
   * if a Floating IP address reservation with the appropriate tags does not exist, create it and add it to the services spec
1. For each service of `type=LoadBalancer` deleted from the cluster:
   * find the Floating IP address from the service spec and remove it
   * delete the Floating IP reservation from Cherry Servers

## Control Plane Load Balancing

CCM implements an optional control plane load balancer using a Cherry Servers Floating IP (FIP) and the Cherry Servers API's
ability to assign that FIP to different devices.

You have several options for control plane load-balancing:

* CCM managed
* No control plane load-balancing (or at least, none known to CCM)

### CCM Managed

It is a common procedure to use a Floating IP as Control Plane endpoint in order to
have a static endpoint that you can use from the outside, or when configuring
the advertise address for the kubelet.

To enable CCM to manage the control plane FIP:

1. Create a Floating IP, using the Cherry Servers API, Web UI or CLI
1. Put an arbitrary but unique tag on the FIP
1. When starting the CCM
   * set the [configuration](#Configuration) for the control plane FIP tag, e.g. env var `CHERRY_FIP_TAG=<tag>`, where `<tag>` is whatever tag you set on the FIP
   * (optional) set the port that the FIP should listen on; by default, or when set to `0`, it will use the same port as the `kube-apiserver` on the control plane nodes. This port can also be specified with `CHERRY_API_SERVER_PORT=<port>.`
   * (optional) set the [configuration](#Configuration) for using the host IP for control plane endpoint health checks. This is
   needed when the FIP is configured as an loopback IP address

Cherry Servers does not provide an as-a-service load balancer; this means that in some way we have to check if the Floating
IP is still assigned to an healthy control plane.

In order to do so CCM implements a reconciliation loop that checks if the
Control Plane Endpoint respond correctly using the `/healthz` endpoint.

When the healthcheck fails CCM looks for the other Control Planes, when it gets
a healthy one it move the FIP to the new device.

This feature by default is disabled and it assumes that the FIP for the
cluster is available and tagged with an arbitrary label.

When the tag is present CCM will filter the available FIPs for the
specified project via tag to lookup the one used by your cluster.

It will check the correct answer, when it stops responding the IP reassign logic
will start.

The logic will circle over all the available control planes looking for an
active api server. As soon as it can find one the FIP will be unassigned
and reassigned to the working node.

#### How the Floating IP Traffic is Routed

Of course, even if the router sends traffic for your Floating IP (FIP) to a given control
plane node, that node needs to know to process the traffic. Rather than require you to
manage the IP assignment on each node, which can lead to some complex timing issues,
the Cherry Servers CCM handles it for you.

The structure relies on the already existing `default/kubernetes` service, which
creates an `Endpoints` structure that includes all of the functioning control plane
nodes. The CCM does the following on each loop:

1. Reads the Kubernetes-created `default/kubernetes` service to discover:
   * what port `kube-apiserver` is listening on from `targetPort`
   * all of the endpoints, i.e. control plane nodes where `kube-apiserver` is running
1. Creates a service named `kube-system/cloud-provider-cherry-kubernetes-external` with the following settings:
   * `type=LoadBalancer`
   * `spec.loadBalancerIP=<fip>`
   * `status.loadBalancer.ingress[0].ip=<fip>`
   * `metadata.annotations["metallb.universe.tf/address-pool"]=disabled-metallb-do-not-use-any-address-pool`
   * `spec.ports[0].targetPort=<targetPort>`
   * `spec.ports[0].port=<targetPort_or_override>`
1. Updates the service `kube-system/cloud-provider-cherry-kubernetes-external` to have endpoints identical to those in `default/kubernetes`

This has the following effect:

* the annotation prevents metallb from trying to manage the IP
* the name prevents CCM from passing it to the loadbalancer provider address mapping, thus preventing any of them from managing it
* the `spec.loadBalancerIP` and `status.loadBalancer.ingress[0].ip` cause kube-proxy to set up routes on all of the nodes
* the endpoints cause the traffic to be routed to the control plane nodes

Note that we _wanted_ to just set `externalIPs` on the original `default/kubernetes`, but that would prevent traffic
from being routed to it from the control nodes, due to iptables rules. LoadBalancer types allow local traffic.

## Core Control Loop

On startup, the CCM sets up the following control loop structures:

1. Implement the [cloud-provider interface](https://pkg.go.dev/k8s.io/cloud-provider#Interface), providing primarily the following API calls:
   * `Initialize()`
   * `InstancesV2()`
   * `LoadBalancer()`
1. In `Initialize`:
   1. If BGP is configured, enable BGP on the project
   1. If FIP control plane management is enabled, create an informer for `Service`, `Node` and `Endpoints`, updating the control plane FIP as needed.

## BGP Configuration

If a loadbalancer is enabled, the CCM enables BGP for the project and enables it by default
on all nodes as they come up. It retrieves the ASNs from the Cherry Servers
API and sets it for each server instance.

The set of servers on which BGP will be enabled can be filtered as well, using the the options in [configuration](#Configuration).
Value for node selector should be a valid Kubernetes label selector (e.g. key1=value1,key2=value2).

## Node Annotations

The Cherry Servers CCM sets Kubernetes annotations on each cluster node.

* Node, or local, ASN, default annotation `cherryservers.com/bgp-peers-{{n}}-node-asn`
* Peer ASN, default annotation `cherryservers.com/bgp-peers-{{n}}-peer-asn`
* Peer IP, default annotation `cherryservers.com/bgp-peers-{{n}}-peer-ip`
* Source IP to use when communicating with peer, default annotation `cherryservers.com/bgp-peers-{{n}}-src-ip`
* CIDR of the private network range in the project which this node is part of, default annotation `cherryservers.com/network-4-private`

These annotation names can be overridden, if you so choose, using the options in [configuration](#Configuration).

Note that the annotations for BGP peering are a _pattern_. There is one annotation per data point per peer,
following the pattern `cherryservers.com/bgp-peers-{{n}}-<info>`, where:

* `{{n}}` is the number of the peer, **always** starting with `0`
* `<info>` is the relevant information, such as `node-asn` or `peer-ip`

For example:

* `cherryservers.com/bgp-peers-0-peer-asn` - ASN of peer 0
* `cherryservers.com/bgp-peers-1-peer-asn` - ASN of peer 1
* `cherryservers.com/bgp-peers-0-peer-ip` - IP of peer 0
* `cherryservers.com/bgp-peers-1-peer-ip` - IP of peer 1

## Floating IP Configuration

If a loadbalancer is enabled, CCM creates a Cherry Servers Floating IP (FIP) reservation for each `Service` of
`type=LoadBalancer`. It tags the Reservation with the following tags:

* `usage="cloud-provider-cherry-auto"`
* `service="<service-hash>"` where `<service-hash>` is the sha256 hash of `<namespace>/<service-name>`. We do this so that the name of the service does not leak out to Cherry Servers itself.
* `cluster=<clusterID>` where `<clusterID>` is the UID of the immutable `kube-system` namespace. We do this so that if someone runs two clusters in the same project, and there is one `Service` in each cluster with the same namespace and name, then the two FIPs will not conflict.

## Running Locally

You can run the CCM locally on your laptop or VM, i.e. not in the cluster. This _dramatically_ speeds up development. To do so:

1. Deploy everything except for the `Deployment` and, optionally, the `Secret`
1. Build it for your local platform `make build`
1. Set the environment variable `CCM_SECRET` to a file with the secret contents as a json, i.e. the content of the secret's `stringData`, e.g. `CCM_SECRET=ccm-secret.yaml`
1. Set the environment variable `KUBECONFIG` to a kubeconfig file with sufficient access to the cluster, e.g. `KUBECONFIG=mykubeconfig`
1. Set the environment variable `CHERRY_REGION_NAME` to the correct region where the cluster is running, e.g. `CHERRY_REGION_NAME="EU-Nord-1`
1. If you want to run a loadbalancer, and it is not yet deployed, deploy it appropriately.
1. Enable the loadbalancer by setting the environment variable `CHERRY_LOAD_BALANCER=metallb://`
1. If you want to use a managed Floating IP for the control plane, create one using the Cherry Servers API or Web UI, tag it uniquely, and set the environment variable `CHERRY_FIP_TAG=<tag>`
1. Run the command.

There are multiple ways to run the command.

In all cases, for lots of extra debugging, add `--v=2` or even higher levels, e.g. `--v=5`.

### Docker

```
docker run --rm -e CHERRY_REGION_NAME=${CHERRY_REGION_NAME} -e CHERRY_LOAD_BALANCER=${CHERRY_LOAD_BALANCER} cherryservers/cloud-provider-cherry:latest --cloud-provider=cherryservers --leader-elect=false --authentication-skip-lookup=true --cloud-config=$CCM_SECRET --kubeconfig=$KUBECONFIG
```

### Go toolchain

```
CHERRY_REGION_NAME=${CHERRY_REGION_NAME} CHERRY_LOAD_BALANCER=${CHERRY_LOAD_BALANCER} go run . --cloud-provider=cherryservers --leader-elect=false --authentication-skip-lookup=true --cloud-config=$CCM_SECRET --kubeconfig=$KUBECONFIG
```

### Locally compiled binary

```
CHERRY_REGION_NAME=${CHERRY_REGION_NAME} CHERRY_LOAD_BALANCER=metallb:// dist/bin/cloud-provider-cherry-darwin-amd64 --cloud-provider=cherryservers --leader-elect=false --authentication-skip-lookup=true --cloud-config=$CCM_SECRET --kubeconfig=$KUBECONFIG
```


