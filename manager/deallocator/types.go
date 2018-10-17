package deallocator

import (
	"github.com/docker/go-events"
	"github.com/docker/swarmkit/api"
	"github.com/docker/swarmkit/manager/state/store"
)

// describes what we need for a service-level resource to be able to deallocate it
// when the service is shut down
// TODO: maybe we'd want to use the same pattern for allocation?
type serviceLevelResource interface {
	id() string

	// should return whether the resource has been marked for deletion
	isPendingDelete() bool
}

type resourceDeallocator struct {
	name string

	// the type of event to watch
	event api.Event

	// should enumerate all existing resources
	enumerator func(readTx store.ReadTx) ([]serviceLevelResource, error)

	// should create a resource from a relevant API event
	// it's _not_ guaranteed to only be passed events of the same type as `event`
	// and should return nil if it can't process the given event
	factory func(event events.Event) serviceLevelResource

	// should return all services currently using the resource
	// with the given ID regardless of their deletion status
	servicesLocator func(tx store.ReadTx, resourceID string) ([]*api.Service, error)

	// should extract the resources contained in a service
	resourcesExtractor func(tx store.ReadTx, service *api.Service) (resources []serviceLevelResource)

	// should delete the resource with the given ID
	deleter func(tx store.Tx, resourceID string) error
}
