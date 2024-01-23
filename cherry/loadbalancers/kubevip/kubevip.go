// kubevip loadbalancer that does nothing, but exists to enable bgp functionality
package kubevip

import (
	"context"

	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers"
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
