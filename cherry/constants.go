package cherry

const (
	cherryIdentifier           = "cloud-provider-cherry-auto"
	cherryTag                  = "usage"
	cherryValue                = cherryIdentifier
	ccmIPDescription           = "Cherry Servers Kubernetes CCM auto-generated for Load Balancer"
	DefaultAnnotationNodeASN   = "cherryservers.com/bgp-peers-{{n}}-node-asn"
	DefaultAnnotationPeerASN   = "cherryservers.com/bgp-peers-{{n}}-peer-asn"
	DefaultAnnotationPeerIP    = "cherryservers.com/bgp-peers-{{n}}-peer-ip"
	DefaultAnnotationSrcIP     = "cherryservers.com/bgp-peers-{{n}}-src-ip"
	DefaultAnnotationBGPPass   = "cherryservers.com/bgp-peers-{{n}}-bgp-pass"
	DefaultAnnotationFIPRegion = "cherryservers.com/fip-region"
)
