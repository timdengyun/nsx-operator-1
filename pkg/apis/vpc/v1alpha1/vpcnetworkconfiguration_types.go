/* Copyright © 2022-2023 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

// +kubebuilder:object:generate=true
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	AccessModePublic  string = "Public"
	AccessModePrivate string = "Private"
)

// VPCNetworkConfigurationSpec defines the desired state of VPCNetworkConfiguration.
// There is a default VPCNetworkConfiguration that applies to Namespaces
// do not have a VPCNetworkConfiguration assigned. When a field is not set
// in a Namespace's VPCNetworkConfiguration, the Namespace will use the value
// in the default VPCNetworkConfiguration.
type VPCNetworkConfigurationSpec struct {
	// PolicyPath of Tier0 or Tier0 VRF gateway.
	DefaultGatewayPath string `json:"defaultGatewayPath,omitempty"`
	// Edge cluster path on which the networking elements will be created.
	EdgeClusterPath string `json:"edgeClusterPath,omitempty"`
	// NSX-T Project the Namespace associated with.
	NSXTProject string `json:"nsxtProject,omitempty"`
	// NSX-T IPv4 Block paths used to allocate external Subnets.
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=5
	ExternalIPv4Blocks []string `json:"externalIPv4Blocks,omitempty"`
	// Private IPv4 CIDRs used to allocate Private Subnets.
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=5
	PrivateIPv4CIDRs []string `json:"privateIPv4CIDRs,omitempty"`
	// Default size of Subnet based upon estimated workload count.
	// Defaults to 32.
	// +kubebuilder:default=32
	DefaultIPv4SubnetSize int `json:"defaultIPv4SubnetSize,omitempty"`
	// DefaultSubnetAccessMode defines the access mode of the default SubnetSet for PodVM and VM.
	// Must be Public or Private.
	// +kubebuilder:validation:Enum=Public;Private
	DefaultSubnetAccessMode string `json:"defaultSubnetAccessMode,omitempty"`
	// ShortID specifies Identifier to use when displaying VPC context in logs.
	// Less than or equal to 8 characters.
	// +kubebuilder:validation:MaxLength=8
	// +optional
	ShortID string `json:"shortID,omitempty"`
	// NSX path of the VPC the Namespace associated with.
	// If vpc is set, only defaultIPv4SubnetSize and defaultSubnetAccessMode
	// take effect, other fields are ignored.
	// +optional
	VPC string `json:"vpc,omitempty"`
}

// VPCNetworkConfigurationStatus defines the observed state of VPCNetworkConfiguration
type VPCNetworkConfigurationStatus struct {
	// VPCs describes VPC info, now it includes lb Subnet info which are needed for AKO.
	VPCs []VPCInfo `json:"vpcs,omitempty"`
}

// VPCInfo defines VPC info needed by tenant admin.
type VPCInfo struct {
	// VPC name.
	Name string `json:"name"`
	// AVISESubnetPath is the NSX Policy Path for the AVI SE Subnet.
	AVISESubnetPath string `json:"lbSubnetPath,omitempty"`
	// NSXLoadBalancerPath is the NSX Policy path for the NSX Load Balancer.
	NSXLoadBalancerPath string `json:"nsxLoadBalancerPath,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:storageversion

// VPCNetworkConfiguration is the Schema for the vpcnetworkconfigurations API.
// +kubebuilder:resource:scope="Cluster"
// +kubebuilder:printcolumn:name="NSXTProject",type=string,JSONPath=`.spec.nsxtProject`,description="NSXTProject the Namespace associated with"
// +kubebuilder:printcolumn:name="ExternalIPv4Blocks",type=string,JSONPath=`.spec.externalIPv4Blocks`,description="ExternalIPv4Blocks assigned to the Namespace"
// +kubebuilder:printcolumn:name="PrivateIPv4CIDRs",type=string,JSONPath=`.spec.privateIPv4CIDRs`,description="PrivateIPv4CIDRs assigned to the Namespace"
type VPCNetworkConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VPCNetworkConfigurationSpec   `json:"spec,omitempty"`
	Status VPCNetworkConfigurationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VPCNetworkConfigurationList contains a list of VPCNetworkConfiguration.
type VPCNetworkConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VPCNetworkConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VPCNetworkConfiguration{}, &VPCNetworkConfigurationList{})
}
