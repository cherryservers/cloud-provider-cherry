// kubevip loadbalancer that does nothing, but exists to enable bgp functionality
package kubevip

import (
	"context"
	"errors"

	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const loadBalancerIPsAnnotation = "kube-vip.io/loadbalancerIPs"

type LB struct {
}

// NewLB returns a new LB
//
//nolint:revive // ignore unused error
func NewLB(k8sclient kubernetes.Interface, config string) *LB {
	return &LB{}
}

// ServiceIP returns the load balancer IP for the Service.
// The boolean result reports whether an IP was found.
//
// The IP is determined as follows:
//  1. If the "kube-vip.io/loadbalancerIPs" annotation is set and non-empty, it is used.
//  2. Otherwise, if svc.Spec.LoadBalancerIP is set, it is used.
func (l *LB) ServiceIP(svc *v1.Service) (string, bool) {
	if svc == nil {
		return "", false
	}

	if ip := svc.Annotations[loadBalancerIPsAnnotation]; ip != "" {
		return ip, true
	}

	if ip := svc.Spec.LoadBalancerIP; ip != "" {
		return ip, true
	}

	return "", false
}

// SetServiceIP sets a service's load balancer IP annotation "kube-vip.io/loadbalancerIPs".
func (l *LB) SetServiceIP(svc *v1.Service, ip string) error {
	if svc == nil {
		return errors.New("failed to set ip, service is nil")
	}

	if svc.Annotations == nil {
		svc.Annotations = make(map[string]string)
	}

	svc.Annotations[loadBalancerIPsAnnotation] = ip
	return nil
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
