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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PromotionSpec defines the desired state of Promotion
type PromotionSpec struct {
	// Copy defines a list of copy operations to perform.
	// +required
	Copy []CopyOperation `json:"copy"`

	// The source environment to promote from.
	// +required
	SourceEnvironment string `json:"sourceEnvironment"`

	// The target environment to promote to.
	// +required
	TargetEnvironment string `json:"targetEnvironment"`

	// Strategy defines the strategy to use when promoting.
	// +required
	Strategy StrategySpec `json:"strategy"`
}

// CopyOperation defines a file/directory copy operation.
type CopyOperation struct {
	// The source path to copy from.
	// +required
	Source string `json:"source"`

	// The target path to copy to.
	// +required
	Target string `json:"target"`
}

type StrategySpec struct {
	PullRequest PullRequestSpec `json:"pullRequest"`
}

const (
	PullRequestProviderGitHub string = "github"
)

type PullRequestSpec struct {
	// The title of the pull request.
	// +required
	Title string `json:"title"`

	// The body of the pull request.
	// +required
	Body string `json:"body"`

	// The Git provider for the pull request.
	// +Kubebuilder:Validation:Enum=github
	Provider string `json:"provider"`

	// SecrefRef reference the secret that contains the API token for the Git provider.
	// +required
	SecretRef corev1.LocalObjectReference `json:"secretRef"`
}

// PromotionStatus defines the observed state of Promotion
type PromotionStatus struct {
	// Pull request branch name.
	// +optional
	PullRequestBranch string `json:"pullRequestBranch,omitempty"`

	// Pull request number.
	// +optional
	PullRequestNumber int `json:"pullRequestNumber,omitempty"`

	// Pull request URL.
	// +optional
	PullRequestURL string `json:"pullRequestURL,omitempty"`

	// Pull request status.
	// +optional
	PullRequestStatus string `json:"pullRequestStatus,omitempty"`

	// Pull request title.
	// +optional
	PullRequestTitle string `json:"pullRequestTitle,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Promotion is the Schema for the promotions API
type Promotion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromotionSpec   `json:"spec,omitempty"`
	Status PromotionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PromotionList contains a list of Promotion
type PromotionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Promotion `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Promotion{}, &PromotionList{})
}
