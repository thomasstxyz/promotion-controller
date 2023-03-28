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
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	securejoin "github.com/cyphar/filepath-securejoin"
	gogitv5 "github.com/go-git/go-git/v5"
	gogitv5config "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogitv5ssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/fluxcd/go-git-providers/gitprovider"

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

	// Defer until the end of this main reconciliation loop.
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

	cleanupTargetBranch, err := r.checkoutTargetEnvironment(ctx, req, prom, sourceEnv, targetEnv)
	if err != nil {
		log.Error(err, "Failed on checkoutTargetEnvironment")
		return ctrl.Result{}, err
	}

	if err := r.copyOperations(ctx, req, prom, sourceEnv, targetEnv); err != nil {
		log.Error(err, "Failed on copyOperations")
		return ctrl.Result{}, err
	}
	if err := r.gitCommitAndPush(ctx, req, prom, sourceEnv, targetEnv); err != nil {
		log.Error(err, "Failed on gitCommitAndPush")
		return ctrl.Result{}, err
	}

	if err := r.pullRequest(ctx, req, prom, sourceEnv, targetEnv); err != nil {
		log.Error(err, "Failed on pullRequest")
		return ctrl.Result{}, err
	}

	cleanupTargetBranch()

	log.Info("End reconciling Promotion", "NamespacedName", req.NamespacedName)

	return ctrl.Result{}, nil
}

func (r *PromotionReconciler) checkoutTargetEnvironment(ctx context.Context, req ctrl.Request, prom *gitopsv1alpha1.Promotion,
	sourceEnv *gitopsv1alpha1.Environment, targetEnv *gitopsv1alpha1.Environment) (func(), error) {
	log := log.FromContext(ctx)

	gitrepo, err := gogitv5.PlainOpen(targetEnv.Status.LocalClonePath)
	if err != nil {
		log.Error(err, "Failed to open cloned git repository")
		return nil, nil
	}
	worktree, err := gitrepo.Worktree()
	if err != nil {
		log.Error(err, "Failed to get worktree")
		return nil, nil
	}
	h, err := gitrepo.Head()
	if err != nil {
		log.Error(err, "Failed to get current HEAD")
		return nil, nil
	}
	originalBranch := h.Name().Short()

	branchNamePattern := fmt.Sprintf("promote-%s-to-%s", sourceEnv.Name, targetEnv.Name)
	if prom.Status.PullRequestBranch != branchNamePattern {
		prom.Status.PullRequestBranch = branchNamePattern
	}

	// Cleanup local branch worktree afterwards.
	cleanupTargetBranch := func() {
		// Reset worktree.
		if err := worktree.Reset(&gogitv5.ResetOptions{
			Mode: gogitv5.HardReset,
		}); err != nil {
			log.Error(err, "Failed to reset worktree")
		}
		// Checkout original branch.
		if err := worktree.Checkout(&gogitv5.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName(originalBranch),
		}); err != nil {
			log.Error(err, "Failed to checkout source branch")
		}
		// Delete local branch.
		if err := gitrepo.Storer.RemoveReference(plumbing.NewBranchReferenceName(prom.Status.PullRequestBranch)); err != nil {
			log.Error(err, "Failed to delete local branch")
		}
		log.Info("Cleaned up target branch", "branch", prom.Status.PullRequestBranch)
	}

	// Checkout Pull Request branch.
	if err := worktree.Checkout(&gogitv5.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(prom.Status.PullRequestBranch),
		Create: true,
		Keep:   true,
	}); err != nil {
		log.Error(err, "Failed to checkout pull request branch")
		return cleanupTargetBranch, nil
	}

	return cleanupTargetBranch, nil
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

// gitCommitAndPush commits all changes.
func (r *PromotionReconciler) gitCommitAndPush(ctx context.Context, req ctrl.Request, prom *gitopsv1alpha1.Promotion,
	sourceEnv *gitopsv1alpha1.Environment, targetEnv *gitopsv1alpha1.Environment) error {

	log := log.FromContext(ctx)

	gitrepo, err := gogitv5.PlainOpen(targetEnv.Status.LocalClonePath)
	if err != nil {
		log.Error(err, "Failed to open cloned repository")
		return err
	}

	worktree, err := gitrepo.Worktree()
	if err != nil {
		log.Error(err, "Failed to get worktree")
		return err
	}

	// Add changes to staging area.
	if err := worktree.AddGlob("."); err != nil {
		log.Error(err, "Failed to add changes to staging area")
		return err
	}

	// Commit changes to branch.
	if _, err := worktree.Commit(fmt.Sprintf("Promote changes from %s to %s",
		sourceEnv.Name, targetEnv.Name), &gogitv5.CommitOptions{
		Author: &object.Signature{
			Name:  "Promotion Bot",
			Email: "bot@promotioncontroller.prototype",
			When:  time.Now(),
		},
		Committer: &object.Signature{
			Name:  "Promotion Bot",
			Email: "bot@example.com",
			When:  time.Now(),
		},
	}); err != nil {
		log.Error(err, "Failed to commit changes")
		return err
	}

	// Fetch kubernetes secret containing the SSHSecret of the target environment.
	sshSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: targetEnv.GetSSHSecretObjectName(), Namespace: targetEnv.Namespace}, sshSecret); err != nil {
		log.Error(err, "Failed to get sshSecret", "sshSecret", targetEnv.GetSSHSecretObjectName())
		return err
	}

	// Transform HTTPS URL to SSH URL (ugly, FIXME).
	sshURL := strings.Replace(targetEnv.Spec.Source.URL, "https://", "git@", 1)
	sshURL = strings.Replace(sshURL, ".com/", ".com:", 1)

	// Create SSH signer.
	sshSigner, err := ssh.ParsePrivateKey(sshSecret.Data["private"])
	if err != nil {
		log.Error(err, "Failed to parse private SSH key")
		return err
	}

	// Push changes to remote, but use SSH Auth and SSH URL.
	if err := gitrepo.Push(&gogitv5.PushOptions{
		Auth: &gogitv5ssh.PublicKeys{
			User:   "git",
			Signer: sshSigner,
		},
		RemoteName: "origin",
		RemoteURL:  sshURL,
		Force:      true,
		RefSpecs: []gogitv5config.RefSpec{
			gogitv5config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", prom.Status.PullRequestBranch, prom.Status.PullRequestBranch)),
		},
		Progress: os.Stdout,
	}); err != nil {
		log.Error(err, "Failed to push changes")
		return err
	}

	return nil
}

// pullRequest creates a pull request for the Promotion.
func (r *PromotionReconciler) pullRequest(ctx context.Context, req ctrl.Request, prom *gitopsv1alpha1.Promotion,
	sourceEnv *gitopsv1alpha1.Environment, targetEnv *gitopsv1alpha1.Environment) error {
	log := log.FromContext(ctx)

	log.Info("Begin Pull Request", "NamespacedName", req.NamespacedName)

	// Fetch kubernetes secret containing the API Token of the target environment.
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: targetEnv.GetAPITokenSecretObjectName(), Namespace: targetEnv.Namespace}, secret); err != nil {
		log.Error(err, "Failed to get secret", "Name", targetEnv.GetAPITokenSecretObjectName())
		return err
	}

	// Create a new git provider client.
	repo, repoInfo, err := NewGitProviderRepository(ctx, targetEnv, secret)
	if err != nil {
		log.Error(err, "Failed to create gitprovider client")
		return err
	}

	if targetEnv.Spec.Source.Provider == gitopsv1alpha1.GitProviderGitHub {
		prom.Status.PullRequestTitle = fmt.Sprintf("Promote changes from %s to %s", sourceEnv.Name, targetEnv.Name)

		var pr gitprovider.PullRequest
		// If the pull request number is set, update the pull request.
		if prom.Status.PullRequestNumber > 0 {
			_, err := repo.PullRequests().Get(ctx, prom.Status.PullRequestNumber)
			if err != nil {
				log.Error(err, "Failed to get pull request", "PullRequestNumber", prom.Status.PullRequestNumber)
				return err
			}

			// Update the pull request
			pr, err = repo.PullRequests().Edit(ctx, prom.Status.PullRequestNumber, gitprovider.EditOptions{
				Title: &prom.Status.PullRequestTitle,
			})
			if err != nil {
				log.Error(err, "Failed to update pull request", "PullRequestNumber", prom.Status.PullRequestNumber)
				return err
			}
			// If the pull request number is not set, create a new pull request.
		} else if prom.Status.PullRequestNumber == 0 {
			// Create a new pull request
			pr, err = repo.PullRequests().Create(ctx, prom.Status.PullRequestTitle, prom.Status.PullRequestBranch, *repoInfo.DefaultBranch, "description here")
			if err != nil {
				log.Error(err, "Failed to create pull request")
				return err
			}
		}

		prom.Status.PullRequestNumber = pr.Get().Number
		prom.Status.PullRequestTitle = pr.Get().Title
		prom.Status.PullRequestURL = pr.Get().WebURL
	}

	log.Info("End Pull Request", "NamespacedName", req.NamespacedName)

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromotionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gitopsv1alpha1.Promotion{}).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(r)
}
