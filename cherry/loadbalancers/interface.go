package loadbalancers

import (
	"context"

	v1 "k8s.io/api/core/v1"
)

type ServiceIPAnnotations interface {
	// ServiceIP returns the load balancer IP for the Service, based on
	// implementation specific annotations.
	//
	// The boolean result reports whether an IP was found.
	ServiceIP(svc *v1.Service) (string, bool)
	// SetServiceIP sets implementation specific load balancer IP annotations.
	SetServiceIP(svc *v1.Service, ip string) error
}

type LB interface {
	// AddService add a service with the provided name and IP
	AddService(ctx context.Context, svcNamespace, svcName, ip string, nodes []Node) error
	// RemoveService remove service with the given IP
	RemoveService(ctx context.Context, svcNamespace, svcName, ip string) error
	// UpdateService ensure that the nodes handled by the service are correct
	UpdateService(ctx context.Context, svcNamespace, svcName string, nodes []Node) error

	ServiceIPAnnotations
}
