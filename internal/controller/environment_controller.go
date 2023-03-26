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
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	gogitv5 "github.com/go-git/go-git/v5"

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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Environment object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.Info("Begin reconciling Environment", "NamespacedName", req.NamespacedName)

	// Get Environment object
	env := &gitopsv1alpha1.Environment{}
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		log.Error(err, "Failed to get Environment")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Defer updating the Environment object until the end of this reconcile.
	defer func() {
		// Update Environment status.
		if err := r.Status().Update(ctx, env); err != nil {
			log.Error(err, "Failed to update Environment status")
			return
		}
	}()

	if err := r.reconcileEnvironment(ctx, req, env); err != nil {
		log.Error(err, "Failed to reconcile environment")
		return ctrl.Result{}, err
	}

	log.Info("End reconciling Environment", "NamespacedName", req.NamespacedName)

	return ctrl.Result{}, nil
}

// reconcileEnvironment clones the environment repository and returns the path to the cloned repository.
func (r *EnvironmentReconciler) reconcileEnvironment(ctx context.Context, req ctrl.Request, env *gitopsv1alpha1.Environment) error {
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

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gitopsv1alpha1.Environment{}).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(r)
}
