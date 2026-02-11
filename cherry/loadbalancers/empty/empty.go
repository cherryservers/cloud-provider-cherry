// empty loadbalancer that does nothing, but exists to enable bgp functionality
package empty

import (
	"context"

	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type LB struct {
}

// NewLB returns a new LB
//
//nolint:revive // ignore unused error
func NewLB(k8sclient kubernetes.Interface, config string) *LB {
	return &LB{}
}

// ServiceIP returns the effective load balancer IP for a service.
func (l *LB) ServiceIP(svc *v1.Service) string {
	if svc == nil {
		return ""
	}
	return svc.Spec.LoadBalancerIP
}

// AddService add a service
//
//nolint:revive // ignore unused error
func (l *LB) AddService(ctx context.Context, svcNamespace, svcName, ip string, nodes []loadbalancers.Node) error {
	return nil
}

// RemoveService remove a service
//
//nolint:revive // ignore unused error
func (l *LB) RemoveService(ctx context.Context, svcNamespace, svcName, ip string) error {
	return nil
}

// UpdateService update a service
//
//nolint:revive // ignore unused error
func (l *LB) UpdateService(ctx context.Context, svcNamespace, svcName string, nodes []loadbalancers.Node) error {
	return nil
}
