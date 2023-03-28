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

package controller

import (
	"context"
	// "fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/fluxcd/go-git-providers/github"
	"github.com/fluxcd/go-git-providers/gitprovider"
	// gogithub "github.com/google/go-github/v49/github"

	gitopsv1alpha1 "github.com/thomasstxyz/promotion-controller/api/v1alpha1"
)

func NewGitProviderRepository(ctx context.Context, env *gitopsv1alpha1.Environment,
	secret *corev1.Secret) (gitprovider.OrgRepository, gitprovider.RepositoryInfo, error) {
	log := log.FromContext(ctx)

	var c gitprovider.Client
	var gitProviderRepo gitprovider.OrgRepository
	var repoInfo gitprovider.RepositoryInfo

	// Create a new client for the provider.
	if env.Spec.Source.Provider == gitopsv1alpha1.GitProviderGitHub {
		var err error
		c, err = github.NewClient(gitprovider.WithOAuth2Token(string(secret.Data["token"])))
		if err != nil {
			log.Error(err, "Failed to create GitHub client")
			return gitProviderRepo, repoInfo, err
		}
	}

	// Parse the URL into an OrgRepositoryRef
	ref, err := gitprovider.ParseOrgRepositoryURL(env.Spec.Source.URL)
	if err != nil {
		log.Error(err, "Failed to parse repository URL", "URL", env.Spec.Source.URL)
		return gitProviderRepo, repoInfo, err
	}
	// Get public information about the git repository.
	gitProviderRepo, err = c.OrgRepositories().Get(ctx, *ref)
	if err != nil {
		log.Error(err, "Failed to get repository", "OrgRepositoryRef", ref)
		return gitProviderRepo, repoInfo, err
	}
	// Use .Get() to aquire a high-level gitprovider.OrganizationInfo struct
	repoInfo = gitProviderRepo.Get()
	// Cast the internal object to a *gogithub.Repository to access custom data
	// internalRepo := gitProviderRepo.APIObject().(*gogithub.Repository)
	// If *repoInfo.Description is empty, a panic will occur, because of nil pointer dereference.
	// fmt.Printf("Description: %s. Homepage: %s", *repoInfo.Description, internalRepo.GetHomepage())
	// Output: Description: Bla bla.. Homepage: https://home.page

	// ---

	return gitProviderRepo, repoInfo, nil
}
