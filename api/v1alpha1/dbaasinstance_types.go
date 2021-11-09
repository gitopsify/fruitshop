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

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

//+operator-sdk:csv:customresourcedefinitions:displayName="DBaaSInstance"
// DBaaSInstance is the Schema for the dbaasinstances API
type DBaaSInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DBaaSInstanceSpec   `json:"spec,omitempty"`
	Status DBaaSInstanceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DBaaSInstanceList contains a list of DBaaSInstance
type DBaaSInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DBaaSInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DBaaSInstance{}, &DBaaSInstanceList{})
}
