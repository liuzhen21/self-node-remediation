/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ResourceDeletionRemediationStrategy       = RemediationStrategyType("ResourceDeletion")
	DeprecatedNodeDeletionRemediationStrategy = RemediationStrategyType("NodeDeletion")

	// SnrConditionProcessing is the condition type used to signal NHC the remediation status
	SnrConditionProcessing = "Processing"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type RemediationStrategyType string

// SelfNodeRemediationSpec defines the desired state of SelfNodeRemediation
type SelfNodeRemediationSpec struct {
	//RemediationStrategy is the remediation method for unhealthy nodes
	//currently "NodeDeletion" is deprecated and "ResourceDeletion" will always happen, regardless of which strategy is selected
	//it will iterate over all pods and volume attachments related to the unhealthy node and delete them
	// +kubebuilder:default:="ResourceDeletion"
	RemediationStrategy RemediationStrategyType `json:"remediationStrategy,omitempty"`
}

// SelfNodeRemediationStatus defines the observed state of SelfNodeRemediation
type SelfNodeRemediationStatus struct {
	//TimeAssumedRebooted is the time by then the unhealthy node assumed to be rebooted
	// +optional
	//+operator-sdk:csv:customresourcedefinitions:type=status
	TimeAssumedRebooted *metav1.Time `json:"timeAssumedRebooted,omitempty"`

	// Phase represents the current phase of remediation,
	// One of: TBD
	// +optional
	//+operator-sdk:csv:customresourcedefinitions:type=status
	Phase *string `json:"phase,omitempty"`

	// LastError captures the last error that occurred during remediation.
	// If no error occurred it would be empty
	//+operator-sdk:csv:customresourcedefinitions:type=status
	LastError string `json:"lastError,omitempty"`

	// +operator-sdk:csv:customresourcedefinitions:type=status,displayName="conditions",xDescriptors="urn:alm:descriptor:com.tectonic.ui:conditions"
	// Represents the observations of a SelfNodeRemediation's current state.
	// Known .status.conditions.type are: "Processing"
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=snr;snremediation

// SelfNodeRemediation is the Schema for the selfnoderemediations API
// +operator-sdk:csv:customresourcedefinitions:resources={{"SelfNodeRemediation","v1alpha1","selfnoderemediations"}}
type SelfNodeRemediation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SelfNodeRemediationSpec   `json:"spec,omitempty"`
	Status SelfNodeRemediationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// SelfNodeRemediationList contains a list of SelfNodeRemediation
type SelfNodeRemediationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SelfNodeRemediation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SelfNodeRemediation{}, &SelfNodeRemediationList{})
}
