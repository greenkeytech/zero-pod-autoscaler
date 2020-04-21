// Copyright 2020 GreenKey Technologies
//
// Licensed under the Apache License, Version 2.0 (the "License"); you
// may not use this file except in compliance with the License.  You
// may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.  See the License for the specific language governing
// permissions and limitations under the License.
package scaler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

var KeyScaleDownAt = "zero-pod-autoscaler/scale-down-at"

type Scaler struct {
	Client    kubernetes.Interface
	Namespace string
	Name      string

	// Target address to which requests are proxied. We need this
	// because endpoints reporting available doesn't mean the
	// Service can actually handle a request... so we ping the
	// actual target also.
	Target string

	TTL             time.Duration
	ensureAvailable chan (chan bool)
	scaleUp         chan int
	connectionInc   chan int

	updated chan interface{}
	deleted chan interface{}
}

func New(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	deployOptions, epOptions metav1.ListOptions,
	target string,
	ttl time.Duration,
) (*Scaler, error) {
	var deploy appsv1.Deployment
	var ep corev1.Endpoints

	if list, err := client.AppsV1().Deployments(namespace).List(deployOptions); err != nil {
		return nil, err
	} else {
		if len(list.Items) > 1 {
			return nil, fmt.Errorf("matched %d Deployments", len(list.Items))
		}

		if len(list.Items) == 0 {
			return nil, fmt.Errorf("did not match any Deployments")
		}

		deploy = list.Items[0]
	}

	if list, err := client.CoreV1().Endpoints(namespace).List(epOptions); err != nil {
		if err != nil {
			return nil, err
		}
	} else {
		if len(list.Items) > 1 {
			return nil, fmt.Errorf("matched %d Endpoints", len(list.Items))
		}

		if len(list.Items) == 0 {
			return nil, fmt.Errorf("did not match any Endpoints")
		}

		ep = list.Items[0]
	}

	log.Printf("watching %s/%s", "Endpoints", ep.Name)
	log.Printf("watching %s/%s", "Deployment", deploy.Name)

	fieldSelector := fmt.Sprintf("metadata.name=%s", deploy.Name)

	factory := informers.NewSharedInformerFactoryWithOptions(
		client,
		1*time.Minute,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.FieldSelector = fieldSelector
		}))

	updated := make(chan interface{})
	deleted := make(chan interface{})
	ensureAvailable := make(chan (chan bool))
	scaleUp := make(chan int)
	connectionInc := make(chan int)

	funcs := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			updated <- obj
		},
		UpdateFunc: func(oldObj, obj interface{}) {
			updated <- obj
		},
		DeleteFunc: func(obj interface{}) {
			deleted <- obj
		},
	}

	if informer := factory.Apps().V1().Deployments().Informer(); true {
		informer.AddEventHandler(funcs)
		go informer.Run(ctx.Done())
	}

	if informer := factory.Core().V1().Endpoints().Informer(); true {
		informer.AddEventHandler(funcs)
		go informer.Run(ctx.Done())
	}

	sc := &Scaler{
		Client:          client,
		Namespace:       namespace,
		Name:            deploy.Name,
		Target:          target,
		TTL:             ttl,
		ensureAvailable: ensureAvailable,
		scaleUp:         scaleUp,
		connectionInc:   connectionInc,
		updated:         updated,
		deleted:         deleted,
	}
	go sc.loop(ctx)

	return sc, nil
}

func (sc *Scaler) TryConnect(ctx context.Context) error {
	dialer := net.Dialer{}
	timeout_ms := 10.0
	factor := 5.0 // timeout series: 10, 50, 250, 1250, 6250, 31250
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		subctx, _ := context.WithTimeout(ctx, time.Duration(timeout_ms)*time.Millisecond)
		timeout_ms *= factor
		conn, err := dialer.DialContext(subctx, "tcp", sc.Target)
		if err != nil {
			log.Printf("failed test connection to %s: %v", sc.Target, err)
			continue
		}

		conn.Close()
		return nil
	}
}

func (sc *Scaler) loop(ctx context.Context) {
	replicas := int32(-1)
	readyAddresses := -1
	notReadyAddresses := -1
	waiters := make([]chan<- bool, 0)
	connCount := 0

	resourceVersion := ""

	scaleDownAt := time.Now().Add(sc.TTL)

	for {
		select {
		case <-ctx.Done():
			log.Printf("context canceled; scaler loop exiting")
			return
		case i := <-sc.connectionInc:
			connCount += i
		case obj := <-sc.updated:
			switch resource := obj.(type) {
			case *corev1.Endpoints:
				r := 0
				nr := 0

				for _, subset := range resource.Subsets {
					r += len(subset.Addresses)
					nr += len(subset.NotReadyAddresses)
				}

				if r != readyAddresses || nr != notReadyAddresses {
					log.Printf("%s/%s: readyAddresses=%d notReadyAddresses=%d",
						"Endpoints", resource.Name, r, nr)
				}

				readyAddresses, notReadyAddresses = r, nr

				if readyAddresses == 0 {
					continue
				}

				if len(waiters) == 0 {
					continue
				}

				log.Printf("%s/%s has ready addresses; confirming can connect to %s",
					"Endpoints", resource.Name, sc.Target)

				subctx, _ := context.WithTimeout(ctx, 15*time.Second)
				if err := sc.TryConnect(subctx); err != nil {
					log.Fatalf("%s/%s has ready addresses but failed to connect to %s: %v",
						"Endpoints", resource.Name, sc.Target, err)
				}

				log.Printf("%s connectable; notifying waiters: %d",
					resource.Name, len(waiters))

				for _, w := range waiters {
					w <- true
				}

				waiters = make([]chan<- bool, 0)
			case *appsv1.Deployment:
				resourceVersion = resource.ResourceVersion

				if timestamp, ok := resource.Annotations[KeyScaleDownAt]; ok {
					if t, err := time.Parse(time.RFC3339, timestamp); err == nil {
						scaleDownAt = t
					}
				}

				if resource.Spec.Replicas != nil {
					if replicas != *resource.Spec.Replicas {
						log.Printf("%s/%s: replicas: %d",
							"Deployment", resource.Name, *resource.Spec.Replicas)
					}
					replicas = *resource.Spec.Replicas
				}
			}
		case obj := <-sc.deleted:
			switch resource := obj.(type) {
			case *corev1.Endpoints:
				log.Fatalf("%s/%s: deleted", "Endpoints", resource.Name)
			case *appsv1.Deployment:
				log.Fatalf("%s/%s: deleted", "Deployment", resource.Name)
			}
		case reply := <-sc.ensureAvailable:
			// set time to scale down
			sc.extendScaleDownAtMaybe(scaleDownAt)

			if readyAddresses > 0 {
				reply <- true
				continue
			}

			// nothing ready; wait for scale up

			waiters = append(waiters, reply)

			if replicas == 0 {
				go func() { sc.scaleUp <- 0 }()

			}
		case attemptNumber := <-sc.scaleUp:
			if replicas == 0 {
				if err := sc.updateScale(resourceVersion, 1); err != nil {
					log.Printf("%s/%s: failed to scale up: %v %T",
						"Deployment", sc.Name, err, err)
					// try again; usual error is that resource is out of date
					// TODO: try again ONLY when error is that resource is out of date
					if attemptNumber < 10 {
						go func() { sc.scaleUp <- attemptNumber + 1 }()
					}
				}
			}
		case <-time.After(1 * time.Second):
			if connCount > 0 {
				sc.extendScaleDownAtMaybe(scaleDownAt)
			}

			if connCount == 0 && replicas > 0 && time.Now().After(scaleDownAt) {
				log.Printf("%s/%s: scaling down after %s: replicas=%d connections=%d",
					"Deployment", sc.Name, sc.TTL, replicas, connCount)

				if err := sc.updateScale(resourceVersion, 0); err != nil {
					log.Printf("%s/%s: failed to scale to zero: %v",
						"Deployment", sc.Name, err)
				}
			}
		}
	}
}

func (sc *Scaler) extendScaleDownAtMaybe(scaleDownAt time.Time) {
	if !time.Now().After(scaleDownAt.Add(sc.TTL / -2)) {
		return
	}

	path := fmt.Sprintf("/metadata/annotations/%s", JsonPatchEscape(KeyScaleDownAt))

	patch := []map[string]string{
		{
			"op":    "replace",
			"path":  path,
			"value": time.Now().Add(sc.TTL).Format(time.RFC3339),
		},
	}

	body, err := json.Marshal(patch)
	if err != nil {
		log.Printf("failed to marshal patch to json: %v", err)
	}

	if _, err := sc.Client.AppsV1().Deployments(sc.Namespace).
		Patch(sc.Name, types.JSONPatchType, body); err != nil {
		log.Printf("%s/%s: failed to patch: %v",
			"Deployment", sc.Name, err)
	}

	log.Printf("%s/%s: updated scaleDownAt to %s from now",
		"Deployment", sc.Name, sc.TTL)
}

func (sc *Scaler) updateScale(resourceVersion string, replicas int32) error {
	deployments := sc.Client.AppsV1().Deployments(sc.Namespace)

	scale := autoscalingv1.Scale{}
	scale.Namespace = sc.Namespace
	scale.Name = sc.Name
	scale.ResourceVersion = resourceVersion

	scale.Spec.Replicas = replicas

	if _, err := deployments.UpdateScale(sc.Name, &scale); err != nil {
		return err
	}

	log.Printf("%s/%s: scaled to %d", "Deployment", sc.Name, replicas)

	return nil
}

func (sc *Scaler) WithAvailable(ctx context.Context, f func() error) error {
	sc.connectionInc <- 1
	defer func() { sc.connectionInc <- (-1) }()

	reply := make(chan bool)
	sc.ensureAvailable <- reply

	select {
	case <-reply:
		return f()
	case <-ctx.Done():
		return fmt.Errorf("context done before available")
	}
}
