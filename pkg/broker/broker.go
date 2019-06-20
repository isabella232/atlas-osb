package broker

import (
	"context"
	"fmt"

	"github.com/fabianlindfors/atlas-service-broker/pkg/atlas"
	"github.com/pivotal-cf/brokerapi"
	"github.com/pivotal-cf/brokerapi/domain/apiresponses"
	"go.uber.org/zap"
)

type Broker struct {
	logger *zap.SugaredLogger
	atlas  atlas.Client
}

func NewBroker(client atlas.Client, logger *zap.SugaredLogger) *Broker {
	return &Broker{
		logger: logger,
		atlas:  client,
	}
}

func (b *Broker) Services(ctx context.Context) ([]brokerapi.Service, error) {
	plans := Plans()
	servicePlans := make([]brokerapi.ServicePlan, len(plans))

	for i, plan := range plans {
		servicePlans[i] = brokerapi.ServicePlan{
			ID:          plan.ID,
			Name:        plan.Name,
			Description: plan.Description,
		}
	}

	return []brokerapi.Service{
		brokerapi.Service{
			ID:                   "mongodb",
			Name:                 "mongodb",
			Description:          "DESCRIPTION",
			Bindable:             true,
			InstancesRetrievable: true,
			BindingsRetrievable:  true,
			Metadata:             nil,
			Plans:                servicePlans,
		},
	}, nil
}

// Plan represents a single plan for the service with an associated instance
// size and broker.
type Plan struct {
	ID           string
	Name         string
	Description  string
	Instance     string
	ProviderName string
}

// Provider returns the Atlas provider settings corresponding to the plan.
func (p *Plan) Provider() atlas.Provider {
	return atlas.Provider{
		Name:     p.ProviderName,
		Instance: p.Instance,
		Region:   "EU_WEST_1",
	}
}

// Plans return all available plans across all providers
func Plans() []Plan {
	return append(providerPlans("AWS"), providerPlans("GCP")...)
}

func providerPlans(provider string) []Plan {
	instanceSizes := []string{"M10", "M20"}

	var plans []Plan

	// AWS Instances
	for _, instance := range instanceSizes {
		plans = append(plans, Plan{
			ID:           fmt.Sprintf("%s-%s", provider, instance),
			Name:         fmt.Sprintf("%s-%s", provider, instance),
			Description:  fmt.Sprintf("Instance size %s on %s", instance, provider),
			Instance:     instance,
			ProviderName: provider,
		})
	}

	return plans
}

// findPlan search all available plans by ID
func findPlan(id string) *Plan {
	for _, plan := range Plans() {
		if plan.ID == id {
			return &plan
		}
	}

	return nil
}

func (b *Broker) GetInstance(ctx context.Context, instanceID string) (spec brokerapi.GetInstanceDetailsSpec, err error) {
	b.logger.Infof("Fetching instance \"%s\"", instanceID)
	err = brokerapi.NewFailureResponse(fmt.Errorf("Unknown instance ID %s", instanceID), 404, "get-instance")
	return
}

// Connect/bind an application to an Atlas cluster
// Should create/find a database user and provide a connection URI
// Credentials will be placed in a Kubernetes secret (how do we make this not dependent on K8S?)
func (b *Broker) Bind(ctx context.Context, instanceID string, bindingID string, details brokerapi.BindDetails, asyncAllowed bool) (brokerapi.Binding, error) {
	b.logger.Infof("Creating binding \"%s\" for instance \"%s\" with details %+v", bindingID, instanceID, details)

	return brokerapi.Binding{
		Credentials: brokerapi.BrokerCredentials{
			Username: "username",
			Password: "password",
		},
	}, nil
}

// Disconnect/unbind an application from an Atlas cluster
func (b *Broker) Unbind(ctx context.Context, instanceID string, bindingID string, details brokerapi.UnbindDetails, asyncAllowed bool) (brokerapi.UnbindSpec, error) {
	b.logger.Infof("Releasing binding \"%s\" for instance \"%s\" with details %+v", bindingID, instanceID, details)
	return brokerapi.UnbindSpec{}, nil
}

func (b *Broker) GetBinding(ctx context.Context, instanceID string, bindingID string) (spec brokerapi.GetBindingSpec, err error) {
	b.logger.Infof("Retrieving binding \"%s\" for instance \"%s\"", bindingID, instanceID)
	err = brokerapi.NewFailureResponse(fmt.Errorf("Unknown binding ID %s", bindingID), 404, "get-binding")
	return
}

func (b *Broker) Update(ctx context.Context, instanceID string, details brokerapi.UpdateDetails, asyncAllowed bool) (brokerapi.UpdateServiceSpec, error) {
	b.logger.Infof("Updating instance \"%s\" with details %+v", instanceID, details)
	return brokerapi.UpdateServiceSpec{
		IsAsync: true,
	}, nil
}

func (b *Broker) LastOperation(ctx context.Context, instanceID string, details brokerapi.PollDetails) (brokerapi.LastOperation, error) {
	b.logger.Infof("Fetching state of last operation for instance \"%s\" with details %+v", instanceID, details)
	return brokerapi.LastOperation{
		State: brokerapi.Succeeded,
	}, nil
}

func (b *Broker) LastBindingOperation(ctx context.Context, instanceID string, bindingID string, details brokerapi.PollDetails) (brokerapi.LastOperation, error) {
	panic("not implemented")
}

// atlasToAPIError converts an Atlas error to a OSB response error
func atlasToAPIError(err error) error {
	switch err {
	case atlas.ErrClusterNotFound:
		return apiresponses.ErrInstanceDoesNotExist
	case atlas.ErrClusterAlreadyExists:
		return apiresponses.ErrInstanceAlreadyExists
	}

	// Fall back on invalid params error if no other match
	return apiresponses.ErrRawParamsInvalid
}
