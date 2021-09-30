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

package controllers

import (
	"context"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/RHEcosystemAppEng/dbaas-operator/api/v1alpha1"
	"github.com/RHEcosystemAppEng/dbaas-operator/controllers/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	InventoryReadyType string = "SpecSynced"
)

// DBaaSInventoryReconciler reconciles a DBaaSInventory object
type DBaaSInventoryReconciler struct {
	*DBaaSReconciler
}

//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=*,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=*/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dbaas.redhat.com,resources=*/finalizers,verbs=update
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles/finalizers;rolebindings/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *DBaaSInventoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx, "DBaaS Inventory", req.NamespacedName)

	tenantList, err := r.tenantListByInventoryNS(ctx, req.Namespace)
	if err != nil {
		logger.Error(err, "unable to list tenants")
		return ctrl.Result{}, err
	}

	// continue only if the inventory is in a valid tenant namespace
	if len(tenantList.Items) > 0 {
		var inventory v1alpha1.DBaaSInventory
		if err := r.Get(ctx, req.NamespacedName, &inventory); err != nil {
			if errors.IsNotFound(err) {
				// CR deleted since request queued, child objects getting GC'd, no requeue
				logger.V(1).Info("DBaaS Inventory resource not found, has been deleted")
				return ctrl.Result{}, nil
			}
			logger.Error(err, "Error fetching DBaaS Inventory for reconcile")
			return ctrl.Result{}, err
		}

		//
		// Inventory RBAC
		//
		if err := r.reconcileInventoryRbacObjs(ctx, inventory, tenantList); err != nil {
			return ctrl.Result{}, err
		}

		// Get list of DBaaSInventories from namespace
		var inventoryList v1alpha1.DBaaSInventoryList
		if err := r.List(ctx, &inventoryList, &client.ListOptions{Namespace: req.Namespace}); err != nil {
			logger.Error(err, "Error fetching DBaaS Inventory List for reconcile")
			return ctrl.Result{}, err
		}

		// Reconcile each pertinent tenant to ensure proper RBAC is created
		for _, tenant := range tenantList.Items {
			// should we return anything on err for these reconciles?
			if err := r.reconcileTenantRbacObjs(ctx, tenant, getAllAuthzFromInventoryList(inventoryList, tenant)); err != nil {
				return ctrl.Result{}, err
			}
		}

		//
		// Provider Inventory
		//
		provider, err := r.getDBaaSProvider(inventory.Spec.ProviderRef.Name, ctx)
		if err != nil {
			if errors.IsNotFound(err) {
				logger.Error(err, "Requested DBaaS Provider is not configured in this environment", "DBaaS Provider", inventory.Spec.ProviderRef)
				return ctrl.Result{}, err
			}
			logger.Error(err, "Error reading configured DBaaS Provider", "DBaaS Provider", inventory.Spec.ProviderRef)
			return ctrl.Result{}, err
		}
		logger.V(1).Info("Found DBaaS Provider", "DBaaS Provider", inventory.Spec.ProviderRef)

		providerName := provider.Spec.Provider.Name

		execution := metrics.NewExecution(provider.Spec.Provider.Name, inventory.Kind, inventory.Name, "list_inventories")

		providerInventory := r.createProviderObject(&inventory, provider.Spec.InventoryKind)
		if result, err := r.reconcileProviderObject(providerInventory, r.providerObjectMutateFn(&inventory, providerInventory, inventory.Spec.DeepCopy()), ctx); err != nil {
			if errors.IsConflict(err) {
				logger.V(1).Info("Provider Inventory modified, retry syncing spec")
				return ctrl.Result{Requeue: true}, nil
			}
			logger.Error(err, "Error reconciling the Provider Inventory resource")
			execution.Finish(err)
			return ctrl.Result{}, err
		} else {
			logger.V(1).Info("Provider Inventory resource reconciled", "result", result)
		}

		var DBaaSProviderInventory v1alpha1.DBaaSProviderInventory
		if err := r.parseProviderObject(providerInventory, &DBaaSProviderInventory); err != nil {
			logger.Error(err, "Error parsing the Provider Inventory resource")
			execution.Finish(err)
			return ctrl.Result{}, err
		}
		if err := r.reconcileDBaaSObjectStatus(&inventory, func() error {
			DBaaSProviderInventory.Status.DeepCopyInto(&inventory.Status)
			return nil
		}, ctx); err != nil {
			if errors.IsConflict(err) {
				logger.V(1).Info("DBaaS Inventory modified, retry syncing status")
				return ctrl.Result{Requeue: true}, nil
			}
			logger.Error(err, "Error updating the DBaaS Inventory status")
			execution.Finish(err)
			return ctrl.Result{}, err
		} else {
			execution.Finish(err)
			setInventoryMetrics(&inventory, providerName)
			logger.V(1).Info("DBaaS Inventory status updated")

		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DBaaSInventoryReconciler) SetupWithManager(mgr ctrl.Manager) (controller.Controller, error) {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DBaaSInventory{}).
		Build(r)
}

// gets rbac objects for an inventory's users
func inventoryRbacObjs(inventory v1alpha1.DBaaSInventory, tenantList v1alpha1.DBaaSTenantList) (rbacv1.Role, rbacv1.RoleBinding) {
	role := rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dbaas-" + inventory.Name + "-inventory-viewer",
			Namespace: inventory.Namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{v1alpha1.GroupVersion.Group},
				Resources:     []string{"dbaasinventories", "dbaasinventories/status"},
				ResourceNames: []string{inventory.Name},
				Verbs:         []string{"get"},
			},
		},
	}
	role.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("Role"))

	roleBinding := rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      role.Name + "s",
			Namespace: role.Namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "Role",
			Name:     role.Name,
		},
	}
	roleBinding.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("RoleBinding"))

	// if inventory.Spec.Authz is nil, use tenant defaults for view access to the Inventory object
	var users, groups []string
	if inventory.Spec.Authz.Users == nil && inventory.Spec.Authz.Groups == nil {
		for _, tenant := range tenantList.Items {
			if tenant.Spec.InventoryNamespace == inventory.Namespace {
				users = append(users, tenant.Spec.Authz.Developer.Users...)
				groups = append(groups, tenant.Spec.Authz.Developer.Groups...)
			}
		}
	} else {
		users = inventory.Spec.Authz.Users
		groups = inventory.Spec.Authz.Groups
	}
	users = uniqueStr(users)
	groups = uniqueStr(groups)

	for _, user := range users {
		roleBinding.Subjects = append(roleBinding.Subjects, getSubject(user, role.Namespace, "User"))
	}
	for _, group := range groups {
		roleBinding.Subjects = append(roleBinding.Subjects, getSubject(group, role.Namespace, "Group"))
	}

	return role, roleBinding
}

var inventoryReasonCache metrics.ProviderReasonsCache = metrics.ProviderReasonsCache{}

func setInventoryMetrics(inv *v1alpha1.DBaaSInventory, provider string) {
	for _, cond := range inv.Status.Conditions {
		if cond.Type == InventoryReadyType {
			setMetricsCondInventoryStatus(inv, &cond, provider)
		} else {
			// to be implemented for DBaaS Provider specific status condition
		}
	}
}

func setMetricsCondInventoryStatus(inv *v1alpha1.DBaaSInventory, cond *metav1.Condition, provider string) {
	if cond.Status == metav1.ConditionTrue {
		setInventoryElapsedTime(inv, cond.LastTransitionTime, provider)
		setInventoryStatusReady(inv, 1, provider)
	} else {
		resetInventoryElapsedTime(inv, provider)
		setInventoryStatusReady(inv, 0, provider)
	}
	setInventoryStatusReason(inv, cond.Reason, provider)
}

func setInventoryElapsedTime(inv *v1alpha1.DBaaSInventory, lastTransitionTime metav1.Time, provider string) {
	metrics.InventoryElapsedTime.With(prometheus.Labels{
		"provider":  provider,
		"inventory": inv.Name,
		"namespace": inv.Namespace}).Set(lastTransitionTime.Sub(inv.CreationTimestamp.Time).Seconds())
}

func resetInventoryElapsedTime(inv *v1alpha1.DBaaSInventory, provider string) {
	metrics.InventoryElapsedTime.Delete(prometheus.Labels{
		"provider":  provider,
		"inventory": inv.Name,
		"namespace": inv.Namespace})
}

func setInventoryStatusReady(inventory *v1alpha1.DBaaSInventory, val float64, provider string) {
	metrics.InventoryStatusReady.With(prometheus.Labels{
		"provider":  provider,
		"inventory": inventory.Name,
		"namespace": inventory.Namespace}).Set(val)
}

func resetInventoryStatusReasons(inv *v1alpha1.DBaaSInventory, provider string) {
	for reason := range *inventoryReasonCache.GetProviderReasons(provider) {
		metrics.InventoryStatusReason.With(prometheus.Labels{
			"provider":  provider,
			"inventory": inv.Name,
			"namespace": inv.Namespace,
			"reason":    string(reason)}).Set(0)
	}
}

func setInventoryStatusReason(inv *v1alpha1.DBaaSInventory, reason, provider string) {
	inventoryReasonCache.SetProviderReason(provider, reason)
	resetInventoryStatusReasons(inv, provider)
	metrics.InventoryStatusReason.With(prometheus.Labels{
		"provider":  provider,
		"inventory": inv.Name,
		"namespace": inv.Namespace,
		"reason":    reason}).Set(1)
}
