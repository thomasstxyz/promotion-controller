/*
Copyright 2023.

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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	GitProviderGitHub string = "github"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// EnvironmentSpec defines the desired state of Environment
type EnvironmentSpec struct {
	// Source defines the source of the environment.
	// +required
	Source SourceSpec `json:"source"`
}

type SourceSpec struct {
	// The clone URL of the Git repository (HTTPS).
	// +required
	URL string `json:"url"`

	// Reference specifies the Git reference to resolve and monitor for changes.
	// +required
	Reference GitRepositoryRef `json:"ref"`

	// The path to the directory which represents the environment.
	// +required
	Path string `json:"path"`

	// The Git provider.
	// Required if you want to do pull requests.
	// +Kubebuilder:Validation:Enum=github
	// +optional
	Provider string `json:"provider,omitempty"`

	// SecrefRef reference the secret that contains the API token for the Git provider.
	// Secret is of type generic and has the API token string stored in the "token" key.
	// Required if the repository is private. Required if you want to raise Pull Requests against it.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// GitRepositoryRef specifies the Git reference to resolve and checkout.
type GitRepositoryRef struct {
	// Branch to check out.
	// +required
	Branch string `json:"branch"`
}

// EnvironmentStatus defines the observed state of Environment
type EnvironmentStatus struct {
	// The path to the local clone of the Environment.
	// +optional
	LocalClonePath string `json:"localClonePath,omitempty"`

	// Indicates whether the Environment is ready for promotion.
	// +optional
	Ready bool `json:"ready,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Environment is the Schema for the environments API
type Environment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EnvironmentSpec   `json:"spec,omitempty"`
	Status EnvironmentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EnvironmentList contains a list of Environment
type EnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Environment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Environment{}, &EnvironmentList{})
}

func (e *Environment) GetLocalClonePath() string {
	return e.Status.LocalClonePath
}

func (e *Environment) GetSSHSecretObjectName() string {
	return fmt.Sprintf("%s-ssh", e.Name)
}

func (e *Environment) GetAPITokenSecretObjectName() string {
	return e.Spec.Source.SecretRef.Name
}
