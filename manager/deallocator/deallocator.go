package deallocator

import (
	"context"
	"reflect"

	"github.com/docker/go-events"
	"github.com/docker/swarmkit/api"
	"github.com/docker/swarmkit/log"
	"github.com/docker/swarmkit/manager/state/store"
)

var (
	resourceDeallocators = make([]resourceDeallocator, 0)
)

func registerServiceLevelResource(resourceDeallocator resourceDeallocator) {
	resourceDeallocators = append(resourceDeallocators, resourceDeallocator)
}

// A Deallocator waits for services to fully shutdown (ie no containers left)
// and then proceeds to deallocate service-level resources (e.g. networks),
// and finally services themselves
// in particular, the Deallocator should be the only place where services, or
// service-level resources, are ever deleted!
// it’s worth noting that this new component’s role is quite different from
// the task reaper’s: tasks are purely internal to Swarmkit, and their status
// is entirely managed by the system itself. In contrast, the deallocator is
// responsible for safely deleting entities that are directly controlled by the
// user.
type Deallocator struct {
	store *store.MemoryStore

	// for services that are shutting down, we keep track of how many
	// tasks still exist for them
	services map[string]*serviceWithTaskCounts

	// mainly used for tests, so that we can peek
	// into the DB state in between events
	// the bool notifies whether any DB update was actually performed
	eventChan chan bool

	stopChan chan struct{}
	doneChan chan struct{}
}

type serviceWithTaskCounts struct {
	service   *api.Service
	taskCount int
}

// New creates a new deallocator
func New(store *store.MemoryStore) *Deallocator {
	return &Deallocator{
		store:    store,
		services: make(map[string]*serviceWithTaskCounts),

		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
	}
}

// Run starts the deallocator, which then starts cleaning up services
// and their resources when relevant (ie when no tasks still exist
// for a given service)
// This is a blocking function
func (deallocator *Deallocator) Run(ctx context.Context) error {
	// those are the events we'll be watching
	events := make([]api.Event, len(resourceDeallocators)+2)
	// we want to know when tasks are deleted by the reaper
	events[0] = api.EventDeleteTask{}
	// and when services get marked for deletion
	events[1] = api.EventUpdateService{}
	i := 2
	for _, resourceDeallocator := range resourceDeallocators {
		events[i] = resourceDeallocator.event
		i++
	}

	var allServices []*api.Service
	resourceLists := make([][]serviceLevelResource, len(resourceDeallocators))

	eventsChan, _, err := store.ViewAndWatch(deallocator.store,
		func(readTx store.ReadTx) (err error) {
			// look for services that are marked for deletion
			// there's no index on the `PendingDelete` field,
			// so we just iterate over all of them and filter manually
			// this is okay since we only do this at leadership change
			allServices, err = store.FindServices(readTx, store.All)

			if err != nil {
				log.G(ctx).WithError(err).Error("failed to list services in deallocator init")
				return err
			}

			// now we also need to look at all existing service-level resources
			// that are marked for deletion
			for i, resourceDeallocator := range resourceDeallocators {
				if allResources, err := resourceDeallocator.enumerator(readTx); err == nil {
					resourceLists[i] = allResources
				} else {
					log.G(ctx).WithError(err).Errorf("failed to list %vs in deallocator init", resourceDeallocator.name)
					return err
				}
			}

			return
		},
		events...)

	if err != nil {
		// if we have an error here, we can't proceed any further, let the world burn
		log.G(ctx).WithError(err).Error("failed to initialize the deallocator")
		return err
	}

	defer func() {
		// eventsChanCancel()
		close(deallocator.doneChan)
	}()

	anyUpdated := false
	// now let's populate our internal taskCounts
	for _, service := range allServices {
		if updated, _ := deallocator.processService(ctx, service); updated {
			anyUpdated = true
		}
	}

	// and deallocate resources that are pending for deletion and aren't used any more
	for i, resourceList := range resourceLists {
		for _, resource := range resourceList {
			if updated, _ := deallocator.processResource(ctx, nil, resourceDeallocators[i], resource, nil); updated {
				anyUpdated = true
			}
		}
	}

	// now we just need to wait for events
	deallocator.notifyEventChan(anyUpdated)
	for {
		select {
		case event := <-eventsChan:
			if updated, err := deallocator.processNewEvent(ctx, event); err == nil {
				deallocator.notifyEventChan(updated)
			} else {
				log.G(ctx).WithError(err).Errorf("error processing deallocator event %#v", event)
			}
		case <-deallocator.stopChan:
			return nil
		}
	}
}

// Stop stops the deallocator's routine
// FIXME (jrouge): see the comment on TaskReaper.Stop() and see when to properly stop this
// plus unit test on this!
func (deallocator *Deallocator) Stop() {
	close(deallocator.stopChan)
	<-deallocator.doneChan
}

func (deallocator *Deallocator) notifyEventChan(updated bool) {
	if deallocator.eventChan != nil {
		deallocator.eventChan <- updated
	}
}

func (deallocator *Deallocator) processService(ctx context.Context, service *api.Service) (bool, error) {
	if !service.PendingDelete {
		return false, nil
	}

	var (
		tasks []*api.Task
		err   error
	)

	deallocator.store.View(func(tx store.ReadTx) {
		tasks, err = store.FindTasks(tx, store.ByServiceID(service.ID))
	})

	if err != nil {
		log.G(ctx).WithError(err).Errorf("failed to retrieve the list of tasks for service %v", service.ID)
		// if in doubt, let's proceed to clean up the service anyway
		// better to clean up resources that shouldn't be cleaned up yet
		// than ending up with a service and some resources lost in limbo forever
		return true, deallocator.deallocateService(ctx, service)
	} else if len(tasks) == 0 {
		// no tasks remaining for this service, we can clean it up
		return true, deallocator.deallocateService(ctx, service)
	}
	deallocator.services[service.ID] = &serviceWithTaskCounts{service: service, taskCount: len(tasks)}
	return false, nil
}

func (deallocator *Deallocator) deallocateService(ctx context.Context, service *api.Service) (err error) {
	err = deallocator.store.Update(func(tx store.Tx) error {
		// first, let's delete the service
		var ignoreServiceID *string
		if err := store.DeleteService(tx, service.ID); err != nil {
			// all errors are just for logging here, we do a best effort at cleaning up everything we can
			log.G(ctx).WithError(err).Errorf("failed to delete service record ID %v", service.ID)
			ignoreServiceID = &service.ID
		}

		// then all of its service-level resources, provided no other service uses them
		for _, resourceDeallocator := range resourceDeallocators {
			for _, resource := range resourceDeallocator.resourcesExtractor(tx, service) {
				deallocator.processResource(ctx, tx, resourceDeallocator, resource, ignoreServiceID)
			}
		}

		return nil
	})

	if err != nil {
		log.G(ctx).WithError(err).Errorf("DB error when deallocating service %v", service.ID)
	}
	return
}

// proceeds to deallocating a resource if it's pending deletion and there no
// longer are any services using it
func (deallocator *Deallocator) processResource(ctx context.Context, tx store.Tx, resourceDeallocator resourceDeallocator, resource serviceLevelResource, ignoreServiceID *string) (updated bool, err error) {
	if !resource.isPendingDelete() {
		return
	}

	updateFunc := func(t store.Tx) error {
		services, err := resourceDeallocator.servicesLocator(t, resource.id())

		if err != nil {
			log.G(ctx).WithError(err).Errorf("could not fetch services using %v ID %v", resourceDeallocator.name, resource.id())
			return err
		}

		noMoreServices := len(services) == 0 ||
			len(services) == 1 && ignoreServiceID != nil && services[0].ID == *ignoreServiceID

		if noMoreServices {
			return resourceDeallocator.deleter(t, resource.id())
		}
		return nil
	}

	if tx == nil {
		err = deallocator.store.Update(updateFunc)
	} else {
		err = updateFunc(tx)
	}

	if err != nil {
		log.G(ctx).WithError(err).Errorf("DB error when deallocating %v ID %v", resourceDeallocator.name, resource.id())
	}
	return
}

func (deallocator *Deallocator) processNewEvent(ctx context.Context, event events.Event) (bool, error) {
	switch typedEvent := event.(type) {
	case api.EventDeleteTask:
		serviceID := typedEvent.Task.ServiceID

		if serviceWithCount, present := deallocator.services[serviceID]; present {
			if serviceWithCount.taskCount <= 1 {
				delete(deallocator.services, serviceID)
				return deallocator.processService(ctx, serviceWithCount.service)
			}
			serviceWithCount.taskCount--
		}

		return false, nil
	case api.EventUpdateService:
		return deallocator.processService(ctx, typedEvent.Service)
	default:
		// must be an event handled by a resource deallocator
		for _, resourceDeallocator := range resourceDeallocators {
			if resource := resourceDeallocator.factory(event); resource != nil {
				return deallocator.processResource(ctx, nil, resourceDeallocator, resource, nil)
			}
		}

		// really shouldn't happen
		log.G(ctx).Errorf("deallocator: unknown event type: %v", reflect.TypeOf(event))
		return false, nil
	}
}
