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
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	gogitv5 "github.com/go-git/go-git/v5"

	"github.com/fluxcd/go-git-providers/gitprovider"

	gitopsv1alpha1 "github.com/thomasstxyz/promotion-controller/api/v1alpha1"
)

// EnvironmentReconciler reconciles a Environment object
type EnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=gitops.promotioncontroller.prototype,resources=environments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gitops.promotioncontroller.prototype,resources=environments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=gitops.promotioncontroller.prototype,resources=environments/finalizers,verbs=update

func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.Info("Begin reconciling Environment", "NamespacedName", req.NamespacedName)

	// Get Environment object
	env := &gitopsv1alpha1.Environment{}
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		log.Error(err, "Failed to get Environment")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Defer updating the Environment object until the end of this reconciliation.
	defer func() {
		// Update Environment status.
		if err := r.Status().Update(ctx, env); err != nil {
			log.Error(err, "Failed to update Environment status")
			return
		}
	}()

	if err := r.reconcileAuthConfig(ctx, req, env); err != nil {
		log.Error(err, "Failed to reconcile authentication configuration")
		return ctrl.Result{}, err
	}

	if err := r.reconcileGitRepository(ctx, req, env); err != nil {
		log.Error(err, "Failed to reconcile environment")
		return ctrl.Result{}, err
	}

	log.Info("End reconciling Environment", "NamespacedName", req.NamespacedName)

	return ctrl.Result{}, nil
}

// reconcileAuthConfig reconciles the authentication configuration for the environment's remote git repository.
func (r *EnvironmentReconciler) reconcileAuthConfig(ctx context.Context, req ctrl.Request, env *gitopsv1alpha1.Environment) error {
	log := log.FromContext(ctx)

	if env.Spec.Source.SecretRef == nil {
		log.Info("No secret reference provided, skipping authentication configuration")
		return nil
	}

	// Fetch kubernetes secret containing the GitProvider API Token.
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: env.Spec.Source.SecretRef.Name, Namespace: req.Namespace}, secret); err != nil {
		log.Error(err, "Failed to get secret", "Name", env.Spec.Source.SecretRef.Name)
		return err
	}

	// create a new gitprovider client and repository interface.
	repo, _, err := NewGitProviderRepository(ctx, env, secret)
	if err != nil {
		log.Error(err, "Failed to create gitprovider client")
		return err
	}

	// --- If ssh key pair secret is not found, create it.

	sshSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: env.GetSSHSecretObjectName(), Namespace: req.Namespace}, sshSecret); err != nil {
		if errors.IsNotFound(err) {
			// Create ssh key pair to be used for git pushes.
			pubKey, privKey, err := MakeSSHKeyPair()
			if err != nil {
				log.Error(err, "Failed to create ssh key pair")
				return err
			}

			// Create kubernetes secret containing ssh key pair.
			sshSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      env.GetSSHSecretObjectName(),
					Namespace: req.Namespace,
				},
				StringData: map[string]string{
					"public":  pubKey,
					"private": privKey,
				},
			}
			if err := r.Create(ctx, sshSecret); err != nil {
				log.Error(err, "Failed to create ssh secret")
				return err
			}

			// Fetch newly created object, in order to have the Data map[string][]byte field populated.
			if err := r.Get(ctx, types.NamespacedName{Name: env.GetSSHSecretObjectName(), Namespace: req.Namespace}, sshSecret); err != nil {
				log.Error(err, "Failed to get ssh secret, even after creating it")
				return err
			}
		}
	}

	// Upload the public key to the Git repository.
	_, err = repo.DeployKeys().Create(ctx, gitprovider.DeployKeyInfo{
		Name:     fmt.Sprintf("Promotion Bot (%s)", env.Name),
		Key:      []byte(sshSecret.Data["public"]),
		ReadOnly: &[]bool{false}[0],
	})
	if err == gitprovider.ErrAlreadyExists || strings.Contains(err.Error(), "key is already in use") {
		log.Info("Deploy key already exists", "Name", fmt.Sprintf("Promotion Bot (%s)", env.Name))
	} else if err != nil {
		log.Error(err, "Failed to create deploy key", "Name", fmt.Sprintf("Promotion Bot (%s)", env.Name))
		return err
	}

	return nil
}

// reconcileGitRepository reconciles the environment's remote git repository with a local clone.
func (r *EnvironmentReconciler) reconcileGitRepository(ctx context.Context, req ctrl.Request, env *gitopsv1alpha1.Environment) error {
	log := log.FromContext(ctx)

	// Check if the environment repository has already been cloned.
	// If the environment repository has not been cloned, clone it.
	if _, err := gogitv5.PlainOpen(env.Status.LocalClonePath); err == gogitv5.ErrRepositoryNotExists {
		tmpDir, err := os.MkdirTemp("", req.Namespace+"-"+req.Name+"-")
		if err != nil {
			log.Error(err, "Failed to create temporary directory")
			return err
		}

		if _, err := gogitv5.PlainClone(tmpDir, false, &gogitv5.CloneOptions{
			URL: env.Spec.Source.URL,
		}); err == nil {
			log.Info("Cloned repository successfully")
		} else if err != nil {
			log.Error(err, "Failed to clone git repository")
			return err
		}

		env.Status.LocalClonePath = tmpDir
	}

	gitrepo, err := gogitv5.PlainOpen(env.Status.LocalClonePath)
	if err != nil {
		log.Error(err, "Failed to open cloned repository")
		return err
	}
	worktree, err := gitrepo.Worktree()
	if err != nil {
		log.Error(err, "Failed to get worktree")
		return err
	}

	if err := worktree.Pull(&gogitv5.PullOptions{}); err == gogitv5.NoErrAlreadyUpToDate {
		log.Info("Repository is up to date with remote")
	} else if err != nil {
		log.Error(err, "Failed to pull from remote")
		return err
	}

	// Set the environment's status to ready.
	env.Status.Ready = true

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gitopsv1alpha1.Environment{}).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(r)
}
