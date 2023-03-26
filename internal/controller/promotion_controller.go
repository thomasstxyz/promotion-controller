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
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	securejoin "github.com/cyphar/filepath-securejoin"
	gogitv5 "github.com/go-git/go-git/v5"

	gitopsv1alpha1 "github.com/thomasstxyz/promotion-controller/api/v1alpha1"
	"github.com/thomasstxyz/promotion-controller/internal/fs"
)

// PromotionReconciler reconciles a Promotion object
type PromotionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=gitops.promotioncontroller.prototype,resources=promotions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=gitops.promotioncontroller.prototype,resources=promotions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=gitops.promotioncontroller.prototype,resources=promotions/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Promotion object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *PromotionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.Info("Begin reconciling Promotion", "NamespacedName", req.NamespacedName)

	// Get Promotion object
	prom := &gitopsv1alpha1.Promotion{}
	if err := r.Get(ctx, req.NamespacedName, prom); err != nil {
		log.Error(err, "Failed to get Promotion")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Get source environment
	sourceEnv := &gitopsv1alpha1.Environment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: prom.Namespace, Name: prom.Spec.SourceEnvironment}, sourceEnv); err != nil {
		log.Error(err, "Failed to get source environment")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Get target environment
	targetEnv := &gitopsv1alpha1.Environment{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: prom.Namespace, Name: prom.Spec.TargetEnvironment}, targetEnv); err != nil {
		log.Error(err, "Failed to get target environment")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Defer until the end of this reconciliation.
	defer func() {
		// Update Promotion status.
		if err := r.Status().Update(ctx, prom); err != nil {
			log.Error(err, "Failed to update Promotion status")
			return
		}

		// Reset worktree of target environment.
		gitrepo, err := gogitv5.PlainOpen(targetEnv.Status.LocalClonePath)
		if err != nil {
			log.Error(err, "Failed to open local repository of target environment")
			return
		}
		worktree, err := gitrepo.Worktree()
		if err != nil {
			log.Error(err, "Failed to get worktree")
			return
		}
		if err := worktree.Reset(&gogitv5.ResetOptions{Mode: gogitv5.HardReset}); err != nil {
			log.Error(err, "Failed to reset worktree")
			return
		}
	}()

	// Ensure source environment is ready.
	if !sourceEnv.Status.Ready {
		log.Info("Source environment is not ready", "ready", sourceEnv.Status.Ready)
		return ctrl.Result{}, nil
	}

	// Ensure target environment is ready.
	if !targetEnv.Status.Ready {
		log.Info("Target environment is not ready", "ready", targetEnv.Status.Ready)
		return ctrl.Result{}, nil
	}

	if err := r.copyOperations(ctx, req, prom, sourceEnv, targetEnv); err != nil {
		log.Error(err, "Failed to do copy operations")
		return ctrl.Result{}, err
	}

	if err := r.pullRequest(ctx, req, prom); err != nil {
		log.Error(err, "Failed to create pull request")
		return ctrl.Result{}, err
	}

	log.Info("End reconciling Promotion", "NamespacedName", req.NamespacedName)

	return ctrl.Result{}, nil
}

// copyOperations performs the copy operations for the Promotion.
func (r *PromotionReconciler) copyOperations(ctx context.Context, req ctrl.Request, prom *gitopsv1alpha1.Promotion,
	sourceEnv *gitopsv1alpha1.Environment, targetEnv *gitopsv1alpha1.Environment) error {
	log := log.FromContext(ctx)

	log.Info("Begin copy operations", "NamespacedName", req.NamespacedName)

	// Securely join LocalClonePath of Git Repo with relative path to environment.
	sourceEnvPath, err := securejoin.SecureJoin(sourceEnv.Status.LocalClonePath, sourceEnv.Spec.Source.Path)
	if err != nil {
		log.Error(err, "Failed to secure join source path", "LocalClonePath", sourceEnv.Status.LocalClonePath, "Path", sourceEnv.Spec.Source.Path)
		return err
	}
	targetEnvPath, err := securejoin.SecureJoin(targetEnv.Status.LocalClonePath, targetEnv.Spec.Source.Path)
	if err != nil {
		log.Error(err, "Failed to secure join target path", "LocalClonePath", targetEnv.Status.LocalClonePath, "Path", targetEnv.Spec.Source.Path)
		return err
	}

	for _, op := range prom.Spec.Copy {
		log.Info("Copy operation", "source", op.Source, "target", op.Target)

		// Securely join relative path of copy operation with source environment path.
		sp, err := securejoin.SecureJoin(sourceEnvPath, op.Source)
		if err != nil {
			log.Error(err, "Failed to secure join source path", "sourceEnvPath", sourceEnvPath, "source", op.Source)
			return err
		}
		tp, err := securejoin.SecureJoin(targetEnvPath, op.Target)
		if err != nil {
			log.Error(err, "Failed to secure join target path", "targetEnvPath", targetEnvPath, "target", op.Target)
			return err
		}

		if f, err := os.Stat(sp); err != nil {
			log.Error(err, "Failed to get file info. Does the specified source path exist?", "source", sp)
			continue
		// If the source path is a directory, copy the directory.
		} else if f.IsDir() {
			log.Info("Copying directory", "source", sp, "target", tp)
			if err := fs.CopyDir(sp, tp); err != nil {
				log.Error(err, "Failed to copy directory", "source", sp, "target", tp)
				return err
			}
		// If the source path is a file, copy the file.
		} else {
			log.Info("Copying file", "source", sp, "target", tp)

			// If target path is a directory, append the file name to the target path.
			if f, err := os.Stat(tp); err == nil && f.IsDir() {
				tp = filepath.Join(tp, filepath.Base(sp))
			}

			// Create target directory if it does not exist.
			if err := os.MkdirAll(filepath.Dir(tp), 0755); err != nil {
				log.Error(err, "Failed to create target directory", "target", tp)
				return err
			}

			if err := fs.CopyFile(sp, tp); err != nil {
				log.Error(err, "Failed to copy file", "source", sp, "target", tp)
				return err
			}
		}
	}

	return nil
}

// pullRequest creates a pull request for the Promotion.
func (r *PromotionReconciler) pullRequest(ctx context.Context, req ctrl.Request, prom *gitopsv1alpha1.Promotion) error {
	log := log.FromContext(ctx)

	log.Info("Begin Pull Request", "NamespacedName", req.NamespacedName)

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromotionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gitopsv1alpha1.Promotion{}).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(r)
}
