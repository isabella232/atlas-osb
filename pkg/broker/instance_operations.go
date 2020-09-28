// Copyright 2020 MongoDB Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mongodb/atlas-osb/pkg/broker/dynamicplans"
	"github.com/mongodb/atlas-osb/pkg/broker/statestorage"
	"github.com/pivotal-cf/brokerapi/domain"
	"github.com/pivotal-cf/brokerapi/domain/apiresponses"
	"github.com/pkg/errors"
	"go.mongodb.org/atlas/mongodbatlas"
)

// The different async operations that can be performed.
// These constants are returned during provisioning, deprovisioning, and
// updates and are subsequently included in async polls from the platform.
const (
	operationProvision   = "provision"
	operationDeprovision = "deprovision"
	operationUpdate      = "update"

	overrideAtlasUserRole = "overrideAtlasUserRole"
)

// Provision will create a new Atlas cluster with the instance ID as its name.
// The process is always async.
func (b Broker) Provision(ctx context.Context, instanceID string, details domain.ProvisionDetails, asyncAllowed bool) (spec domain.ProvisionedServiceSpec, err error) {
	logger := b.funcLogger().With("instance_id", instanceID)

	logger.Infow("Provisioning instance", "details", details)

	planContext := dynamicplans.Context{
		"instance_id": instanceID,
	}
	if len(details.RawParameters) > 0 {
		err = json.Unmarshal(details.RawParameters, &planContext)
		if err != nil {
			return
		}
	}

	if len(details.RawContext) > 0 {
		err = json.Unmarshal(details.RawContext, &planContext)
		if err != nil {
			return
		}
	}

	client, dp, err := b.getClient(ctx, instanceID, details.PlanID, planContext)
	if err != nil {
		return
	}

	if dp.Project.ID == "" {
		var newp *mongodbatlas.Project
		newp, err = b.createResources(ctx, client, dp)
		if err != nil {
			return
		}

		dp.Project.ID = newp.ID
	}

	// Async needs to be supported for provisioning to work.
	if !asyncAllowed {
		err = apiresponses.ErrAsyncRequired
		return
	}

	// Construct a cluster definition from the instance ID, service, plan, and params.
	logger.Infow("Creating cluster", "instance_name", planContext["instance_name"])
	// TODO - add this context info about k8s/namespace or pcf space into labels

	planEnc, err := encodePlan(*dp)
	if err != nil {
		return
	}

	s := domain.GetInstanceDetailsSpec{
		PlanID:       details.PlanID,
		ServiceID:    details.ServiceID,
		DashboardURL: b.GetDashboardURL(dp.Project.ID, dp.Cluster.Name),
		Parameters:   planEnc,
	}

	state, err := b.getState(dp.Project.OrgID)
	if err != nil {
		return
	}

	v, err := state.Put(context.Background(), instanceID, &s)
	if err != nil {
		logger.Errorw("Error during provision, broker maintenance:", "err", err)
		return
	}
	logger.Infow("Inserted new state value", "v", v)

	defer func() {
		if err != nil {
			_ = state.DeleteOne(ctx, instanceID)
		}
	}()

	// Create a new Atlas cluster from the generated definition
	resultingCluster, _, err := client.Clusters.Create(ctx, dp.Project.ID, dp.Cluster)

	if err != nil {
		logger.Errorw("Failed to create Atlas cluster", "error", err, "cluster", dp.Cluster)
		return
	}

	logger.Infow("Successfully started Atlas creation process", "cluster", resultingCluster)

	return domain.ProvisionedServiceSpec{
		IsAsync:       true,
		OperationData: operationProvision,
		DashboardURL:  b.GetDashboardURL(dp.Project.ID, resultingCluster.Name),
	}, nil
}

func (b *Broker) createResources(ctx context.Context, client *mongodbatlas.Client, dp *dynamicplans.Plan) (*mongodbatlas.Project, error) {
	logger := b.funcLogger()

	p, _, err := client.Projects.Create(ctx, dp.Project)
	if err != nil {
		logger.Errorw("Cannot create project", "error", err, "project", dp.Project)
		return nil, err
	}

	for _, u := range dp.DatabaseUsers {
		_, _, err := client.DatabaseUsers.Create(ctx, p.ID, u)
		if err != nil {
			return nil, err
		}
	}

	if len(dp.IPWhitelists) > 0 {
		_, _, err := client.ProjectIPWhitelist.Create(ctx, p.ID, dp.IPWhitelists)
		if err != nil {
			return nil, err
		}
	}

	return p, nil
}

// Update will change the configuration of an existing Atlas cluster asynchronously.
func (b Broker) Update(ctx context.Context, instanceID string, details domain.UpdateDetails, asyncAllowed bool) (spec domain.UpdateServiceSpec, err error) {
	logger := b.funcLogger().With("instance_id", instanceID)
	logger.Infow("Updating instance", "details", details)

	planContext := dynamicplans.Context{
		"instance_id": instanceID,
	}

	if len(details.RawParameters) > 0 {
		err = json.Unmarshal(details.RawParameters, &planContext)
		if err != nil {
			return
		}
	}

	if len(details.RawContext) > 0 {
		err = json.Unmarshal(details.RawContext, &planContext)
		if err != nil {
			return
		}
	}

	logger.Infow("Update() planContext merged with details.parameters&context", "planContext", planContext)
	client, oldPlan, err := b.getClient(ctx, instanceID, details.PlanID, planContext)
	if err != nil {
		return
	}

	// Async needs to be supported for provisioning to work.
	if !asyncAllowed {
		err = apiresponses.ErrAsyncRequired
		return
	}

	// special case: pause/unpause
	if paused, ok := planContext["paused"].(bool); ok {
		request := &mongodbatlas.Cluster{
			Paused: &paused,
		}

		_, _, err = client.Clusters.Update(ctx, oldPlan.Project.ID, oldPlan.Cluster.Name, request)
		return domain.UpdateServiceSpec{
			IsAsync:       true,
			OperationData: operationUpdate,
			DashboardURL:  b.GetDashboardURL(oldPlan.Project.ID, oldPlan.Cluster.Name),
		}, err
	}

	// special case: perform update operations
	if op, ok := planContext["op"].(string); ok {
		err = b.performOperation(ctx, planContext, client, oldPlan, op)
		return domain.UpdateServiceSpec{
			IsAsync:       false, // feature spec: "[the broker] will not maintain ANY state of the list of users in an Atlas project", so no "in progress" indicator
			OperationData: operationUpdate,
			DashboardURL:  b.GetDashboardURL(oldPlan.Project.ID, oldPlan.Cluster.Name),
		}, err
	}

	// Fetch the cluster from Atlas. The Atlas API requires an instance size to
	// be passed during updates (if there are other update to the provider, such
	// as region). The plan is not included in the OSB call unless it has changed
	// hence we need to fetch the current value from Atlas.
	existingCluster, _, err := client.Clusters.Get(ctx, oldPlan.Project.ID, oldPlan.Cluster.Name)
	if err != nil {
		return
	}

	newPlan, err := b.parsePlan(planContext, details.PlanID)
	if err != nil {
		return
	}

	// Atlas doesn't allow for cluster renaming - ignore any changes
	newPlan.Cluster.Name = existingCluster.Name

	resultingCluster, _, err := client.Clusters.Update(ctx, oldPlan.Project.ID, existingCluster.Name, newPlan.Cluster)
	if err != nil {
		logger.Errorw("Failed to update Atlas cluster", "error", err, "new_cluster", newPlan.Cluster)
		return
	}

	planEnc, err := encodePlan(*oldPlan)
	if err != nil {
		return
	}

	oldPlan.Cluster = resultingCluster
	s := domain.GetInstanceDetailsSpec{
		PlanID:       details.PlanID,
		ServiceID:    details.ServiceID,
		DashboardURL: b.GetDashboardURL(oldPlan.Project.ID, oldPlan.Cluster.Name),
		Parameters:   planEnc,
	}

	state, err := b.getState(oldPlan.Project.OrgID)
	if err != nil {
		return
	}

	// TODO: make this error-out reversible?
	err = state.DeleteOne(ctx, instanceID)
	if err != nil {
		logger.Errorw("Error delete from state", "err", err)
		return
	}

	obj, err := state.Put(ctx, instanceID, &s)
	if err != nil {
		logger.Errorw("Error insert one from state", "err", err, "s", s)
		return
	}

	logger.Infow("Inserted into state", "obj", obj)
	logger.Infow("Successfully started Atlas cluster update process", "cluster", resultingCluster)

	return domain.UpdateServiceSpec{
		IsAsync:       true,
		OperationData: operationUpdate,
		DashboardURL:  b.GetDashboardURL(oldPlan.Project.ID, resultingCluster.Name),
	}, nil
}

// Deprovision will destroy an Atlas cluster asynchronously.
func (b Broker) Deprovision(ctx context.Context, instanceID string, details domain.DeprovisionDetails, asyncAllowed bool) (spec domain.DeprovisionServiceSpec, err error) {
	logger := b.funcLogger().With("instance_id", instanceID)
	logger.Infow("Deprovisioning instance", "details", details)

	client, p, err := b.getClient(ctx, instanceID, details.PlanID, nil)
	if err != nil {
		return
	}

	// Async needs to be supported for provisioning to work.
	if !asyncAllowed {
		err = apiresponses.ErrAsyncRequired
		return
	}

	_, err = client.Clusters.Delete(ctx, p.Project.ID, p.Cluster.Name)
	if err != nil {
		logger.Errorw("Failed to delete Atlas cluster", "error", err)
	}

	for _, u := range p.DatabaseUsers {
		_, err = client.DatabaseUsers.Delete(ctx, u.DatabaseName, p.Project.ID, u.Username)
		if err != nil {
			logger.Errorw("failed to delete Database user", "error", err, "username", u.Username)
		}
	}

	logger.Infow("Successfully started Atlas Cluster & Project deletion process")

	return domain.DeprovisionServiceSpec{
		IsAsync:       true,
		OperationData: operationDeprovision,
	}, nil
}

// GetInstance should fetch the stored instance from state storage
func (b Broker) GetInstance(ctx context.Context, instanceID string) (spec domain.GetInstanceDetailsSpec, err error) {
	logger := b.funcLogger().With("instanceID", instanceID)
	logger.Info("Fetching instance")

	spec, err = b.getInstance(ctx, instanceID)
	if err != nil {
		logger.Errorw("Unable to fetch instance", "err", err)
		return spec, apiresponses.NewFailureResponse(err, http.StatusInternalServerError, "get-instance")
	}

	return spec, nil
}

func (b Broker) getInstance(ctx context.Context, instanceID string) (spec domain.GetInstanceDetailsSpec, err error) {
	logger := b.funcLogger().With("instanceID", instanceID)

	for k, v := range b.credentials.Keys() {
		logger = logger.With("orgID", k)

		state, err := statestorage.Get(v, b.cfg.AtlasURL, b.cfg.RealmURL, b.logger)
		if err != nil {
			logger.Errorw("Cannot get state storage for org", "error", err)
			continue
		}

		instance, err := state.FindOne(ctx, instanceID)
		if err != nil {
			if err != statestorage.ErrInstanceNotFound {
				logger.Errorw("Cannot find instance in maintenance DB", "error", err)
			}
			continue
		}
		return *instance, nil
	}

	return domain.GetInstanceDetailsSpec{}, errors.New("cannot find instance in maintenance DB(s): no instances found")
}

func (b *Broker) performOperation(ctx context.Context, planContext dynamicplans.Context, client *mongodbatlas.Client, p *dynamicplans.Plan, op string) error {
	userFromContext := func() (u *mongodbatlas.AtlasUser, err error) {
		email, ok := planContext["email"].(string)
		if !ok {
			return nil, fmt.Errorf("email should be string, got %T", planContext["email"])
		}

		password, ok := planContext["password"].(string)
		if !ok {
			password, err = generatePassword()
			if err != nil {
				return nil, err
			}
		}

		role := p.Settings[overrideAtlasUserRole]
		if role == "" {
			role = "GROUP_READ_ONLY"
		}

		return &mongodbatlas.AtlasUser{
			EmailAddress: email,
			Password:     password,
			Country:      "US",
			Username:     email,
			Roles: []mongodbatlas.AtlasRole{
				{
					GroupID:  p.Project.ID,
					RoleName: role,
				},
			},
		}, nil
	}

	switch op {
	case "AddUserToProject":
		u, err := userFromContext()
		if err != nil {
			return err
		}

		_, _, err = client.AtlasUsers.Create(ctx, u)
		if err != nil {
			return err
		}

	case "RemoveUserFromProject":
		// TODO
		email, ok := planContext["email"].(string)
		if !ok {
			return errors.New("")
		}
		u, _, err := client.AtlasUsers.GetByName(ctx, email)
		if err != nil {
			return err
		}

		_, err = client.Projects.RemoveUserFromProject(ctx, p.Project.ID, u.ID)
		return err

	default:
		return fmt.Errorf("unknown operation %q", op)
	}

	return nil
}

// LastOperation should fetch the state of the provision/deprovision
// of a cluster.
func (b Broker) LastOperation(ctx context.Context, instanceID string, details domain.PollDetails) (resp domain.LastOperation, err error) {
	logger := b.funcLogger().With("instance_id", instanceID)
	logger.Infow("Fetching state of last operation", "details", details)

	resp.State = domain.Failed

	// brokerapi will NOT update service state if we return any error, so... we won't?
	defer func() {
		if err != nil {
			resp.State = domain.Failed
			resp.Description = "got error: " + err.Error()
			err = nil
		}
	}()

	client, p, err := b.getClient(ctx, instanceID, details.PlanID, nil)
	if err != nil {
		return
	}

	cluster, r, err := client.Clusters.Get(ctx, p.Project.ID, p.Cluster.Name)
	if err != nil && r.StatusCode != http.StatusNotFound {
		err = errors.Wrap(err, "cannot get existing cluster")
		logger.Errorw("Failed to get existing cluster", "error", err)
		return
	}

	logger.Infow("Found existing cluster", "cluster", cluster)

	switch details.OperationData {
	case operationProvision, operationUpdate:
		if r.StatusCode == http.StatusNotFound {
			resp.State = domain.Failed
			resp.Description = "cluster not found"
			return
		}

		switch cluster.StateName {
		// Provision has succeeded if the cluster is in state "idle".
		case "IDLE":
			resp.State = domain.Succeeded
		case "CREATING", "UPDATING", "REPAIRING":
			resp.State = domain.InProgress
			resp.Description = cluster.StateName
		default:
			resp.Description = fmt.Sprintf("unknown cluster state %q", cluster.StateName)
		}

	case operationDeprovision:
		switch {
		// The Atlas API may return a 404 response if a cluster is deleted or it
		// will return the cluster with a state of "DELETED". Both of these
		// scenarios indicate that a cluster has been successfully deleted.
		case r.StatusCode == http.StatusNotFound, cluster.StateName == "DELETED":
			if r.StatusCode == http.StatusNotFound || cluster.StateName == "DELETED" {
				resp.State = domain.Succeeded
			}

			var r *mongodbatlas.Response
			r, err = client.Projects.Delete(ctx, p.Project.ID)
			if err != nil {
				err = errors.Wrap(err, "cannot delete Atlas project")
				logger.Errorw(
					"Cannot delete Atlas Project",
					"error", err,
					"projectID", p.Project.ID,
					"projectName", p.Project.Name,
				)

				if r.StatusCode != http.StatusNotFound {
					break
				}

				// don't fail if the project is already deleted
				err = nil
			}

			state, errDel := b.getState(p.Project.OrgID)
			if errDel != nil {
				logger.Errorw("Failed to get state storage", "error", errDel)
				break
			}

			errDel = state.DeleteOne(ctx, instanceID)
			if errDel != nil {
				logger.Errorw("Failed to clean up instance from maintenance store", "error", errDel)
				break
			}

		case cluster.StateName == "DELETING":
			resp.State = domain.InProgress

		default:
			resp.Description = fmt.Sprintf("unknown cluster state %q", cluster.StateName)
		}

	default:
		resp.Description = fmt.Sprintf("unknown operation %q", details.OperationData)
	}

	return resp, err
}
