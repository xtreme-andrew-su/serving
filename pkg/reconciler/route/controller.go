/*
Copyright 2019 The Knative Authors

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

package route

import (
	"context"

	netclient "knative.dev/networking/pkg/client/injection/client"
	certificateinformer "knative.dev/networking/pkg/client/injection/informers/networking/v1alpha1/certificate"
	domaininformer "knative.dev/networking/pkg/client/injection/informers/networking/v1alpha1/domain"
	ingressinformer "knative.dev/networking/pkg/client/injection/informers/networking/v1alpha1/ingress"
	realminformer "knative.dev/networking/pkg/client/injection/informers/networking/v1alpha1/realm"
	kubeclient "knative.dev/pkg/client/injection/kube/client"
	serviceinformer "knative.dev/pkg/client/injection/kube/informers/core/v1/service"
	servingclient "knative.dev/serving/pkg/client/injection/client"
	configurationinformer "knative.dev/serving/pkg/client/injection/informers/serving/v1/configuration"
	revisioninformer "knative.dev/serving/pkg/client/injection/informers/serving/v1/revision"
	routeinformer "knative.dev/serving/pkg/client/injection/informers/serving/v1/route"
	routereconciler "knative.dev/serving/pkg/client/injection/reconciler/serving/v1/route"

	"k8s.io/client-go/tools/cache"
	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/system"
	"knative.dev/pkg/tracker"
	v1 "knative.dev/serving/pkg/apis/serving/v1"
	"knative.dev/serving/pkg/network"
	servingreconciler "knative.dev/serving/pkg/reconciler"
	"knative.dev/serving/pkg/reconciler/route/config"
)

const controllerAgentName = "route-controller"

// NewController initializes the controller and is called by the generated code
// Registers eventhandlers to enqueue events
func NewController(
	ctx context.Context,
	cmw configmap.Watcher,
) *controller.Impl {
	return newControllerWithClock(ctx, cmw, system.RealClock{})
}

type reconcilerOption func(*Reconciler)

func newControllerWithClock(
	ctx context.Context,
	cmw configmap.Watcher,
	clock system.Clock,
	opts ...reconcilerOption,
) *controller.Impl {
	ctx = servingreconciler.AnnotateLoggerWithName(ctx, controllerAgentName)
	logger := logging.FromContext(ctx)
	serviceInformer := serviceinformer.Get(ctx)
	routeInformer := routeinformer.Get(ctx)
	configInformer := configurationinformer.Get(ctx)
	revisionInformer := revisioninformer.Get(ctx)
	ingressInformer := ingressinformer.Get(ctx)
	certificateInformer := certificateinformer.Get(ctx)
	realmInformer := realminformer.Get(ctx)
	domainInformer := domaininformer.Get(ctx)

	c := &Reconciler{
		kubeclient:          kubeclient.Get(ctx),
		client:              servingclient.Get(ctx),
		netclient:           netclient.Get(ctx),
		configurationLister: configInformer.Lister(),
		revisionLister:      revisionInformer.Lister(),
		serviceLister:       serviceInformer.Lister(),
		ingressLister:       ingressInformer.Lister(),
		certificateLister:   certificateInformer.Lister(),
		realmLister:         realmInformer.Lister(),
		domainLister:        domainInformer.Lister(),
		clock:               clock,
	}
	impl := routereconciler.NewImpl(ctx, c, func(impl *controller.Impl) controller.Options {
		configsToResync := []interface{}{
			&network.Config{},
			&config.Domain{},
		}
		resync := configmap.TypeFilter(configsToResync...)(func(string, interface{}) {
			impl.GlobalResync(routeInformer.Informer())
		})
		configStore := config.NewStore(logging.WithLogger(ctx, logger.Named("config-store")), resync)
		configStore.WatchConfigs(cmw)
		return controller.Options{ConfigStore: configStore}
	})

	logger.Info("Setting up event handlers")
	routeInformer.Informer().AddEventHandler(controller.HandleAll(impl.Enqueue))

	handleControllerOf := cache.FilteringResourceEventHandler{
		FilterFunc: controller.FilterControllerGK(v1.Kind("Route")),
		Handler:    controller.HandleAll(impl.EnqueueControllerOf),
	}
	serviceInformer.Informer().AddEventHandler(handleControllerOf)
	certificateInformer.Informer().AddEventHandler(handleControllerOf)
	ingressInformer.Informer().AddEventHandler(handleControllerOf)

	c.tracker = tracker.New(impl.EnqueueKey, controller.GetTrackerLease(ctx))

	// Make sure trackers are deleted once the observers are removed.
	routeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: c.tracker.OnDeletedObserver,
	})

	configInformer.Informer().AddEventHandler(controller.HandleAll(
		// Call the tracker's OnChanged method, but we've seen the objects
		// coming through this path missing TypeMeta, so ensure it is properly
		// populated.
		controller.EnsureTypeMeta(
			c.tracker.OnChanged,
			v1.SchemeGroupVersion.WithKind("Configuration"),
		),
	))

	revisionInformer.Informer().AddEventHandler(controller.HandleAll(
		// Call the tracker's OnChanged method, but we've seen the objects
		// coming through this path missing TypeMeta, so ensure it is properly
		// populated.
		controller.EnsureTypeMeta(
			c.tracker.OnChanged,
			v1.SchemeGroupVersion.WithKind("Revision"),
		),
	))

	for _, opt := range opts {
		opt(c)
	}
	return impl
}
