/* Copyright © 2024 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

// Code generated by lister-gen. DO NOT EDIT.

package v1alpha1

import (
	v1alpha1 "github.com/vmware-tanzu/nsx-operator/pkg/apis/vpc/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
)

// IPAddressAllocationLister helps list IPAddressAllocations.
// All objects returned here must be treated as read-only.
type IPAddressAllocationLister interface {
	// List lists all IPAddressAllocations in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.IPAddressAllocation, err error)
	// IPAddressAllocations returns an object that can list and get IPAddressAllocations.
	IPAddressAllocations(namespace string) IPAddressAllocationNamespaceLister
	IPAddressAllocationListerExpansion
}

// iPAddressAllocationLister implements the IPAddressAllocationLister interface.
type iPAddressAllocationLister struct {
	indexer cache.Indexer
}

// NewIPAddressAllocationLister returns a new IPAddressAllocationLister.
func NewIPAddressAllocationLister(indexer cache.Indexer) IPAddressAllocationLister {
	return &iPAddressAllocationLister{indexer: indexer}
}

// List lists all IPAddressAllocations in the indexer.
func (s *iPAddressAllocationLister) List(selector labels.Selector) (ret []*v1alpha1.IPAddressAllocation, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.IPAddressAllocation))
	})
	return ret, err
}

// IPAddressAllocations returns an object that can list and get IPAddressAllocations.
func (s *iPAddressAllocationLister) IPAddressAllocations(namespace string) IPAddressAllocationNamespaceLister {
	return iPAddressAllocationNamespaceLister{indexer: s.indexer, namespace: namespace}
}

// IPAddressAllocationNamespaceLister helps list and get IPAddressAllocations.
// All objects returned here must be treated as read-only.
type IPAddressAllocationNamespaceLister interface {
	// List lists all IPAddressAllocations in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.IPAddressAllocation, err error)
	// Get retrieves the IPAddressAllocation from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*v1alpha1.IPAddressAllocation, error)
	IPAddressAllocationNamespaceListerExpansion
}

// iPAddressAllocationNamespaceLister implements the IPAddressAllocationNamespaceLister
// interface.
type iPAddressAllocationNamespaceLister struct {
	indexer   cache.Indexer
	namespace string
}

// List lists all IPAddressAllocations in the indexer for a given namespace.
func (s iPAddressAllocationNamespaceLister) List(selector labels.Selector) (ret []*v1alpha1.IPAddressAllocation, err error) {
	err = cache.ListAllByNamespace(s.indexer, s.namespace, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.IPAddressAllocation))
	})
	return ret, err
}

// Get retrieves the IPAddressAllocation from the indexer for a given namespace and name.
func (s iPAddressAllocationNamespaceLister) Get(name string) (*v1alpha1.IPAddressAllocation, error) {
	obj, exists, err := s.indexer.GetByKey(s.namespace + "/" + name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1alpha1.Resource("ipaddressallocation"), name)
	}
	return obj.(*v1alpha1.IPAddressAllocation), nil
}
