package deallocator

import (
	"github.com/docker/go-events"
	"github.com/docker/swarmkit/api"
	"github.com/docker/swarmkit/manager/state/store"
)

type networkWrapper struct {
	*api.Network
}

func (network networkWrapper) id() string {
	return network.ID
}

func (network networkWrapper) isPendingDelete() bool {
	return network.PendingDelete
}

func init() {
	registerServiceLevelResource(resourceDeallocator{
		name:  "network",
		event: api.EventUpdateNetwork{},

		enumerator: func(readTx store.ReadTx) (wrappedNetworks []serviceLevelResource, err error) {
			networks, err := store.FindNetworks(readTx, store.All)
			if err != nil {
				return nil, err
			}

			wrappedNetworks = make([]serviceLevelResource, len(networks))
			for i := 0; i < len(networks); i++ {
				wrappedNetworks[i] = networkWrapper{networks[i]}
			}
			return
		},

		factory: func(event events.Event) serviceLevelResource {
			if typedEvent, ok := event.(api.EventUpdateNetwork); ok {
				return networkWrapper{typedEvent.Network}
			}
			return nil
		},

		servicesLocator: func(tx store.ReadTx, networkID string) ([]*api.Service, error) {
			return store.FindServices(tx, store.ByReferencedNetworkID(networkID))
		},

		resourcesExtractor: func(tx store.ReadTx, service *api.Service) []serviceLevelResource {
			spec := service.Spec
			// see https://github.com/docker/swarmkit/blob/e2aafdd3453d2ab103dd97364f79ea6b857f9446/api/specs.proto#L80-L84
			// we really should have a helper function on services to do this...
			networkConfigs := spec.Task.Networks
			if len(networkConfigs) == 0 {
				networkConfigs = spec.Networks
			}

			networks := make([]serviceLevelResource, 0, len(networkConfigs))
			for _, networkConfig := range networkConfigs {
				if network := store.GetNetwork(tx, networkConfig.Target); network != nil {
					networks = append(networks, networkWrapper{network})
				}
			}
			return networks
		},

		deleter: func(tx store.Tx, networkID string) error {
			return store.DeleteNetwork(tx, networkID)
		},
	})
}
