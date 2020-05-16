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
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	// required to work with clientcmd/api
	flag "github.com/spf13/pflag"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/greenkeytech/zero-pod-autoscaler/pkg/kubeconfig"
	"github.com/greenkeytech/zero-pod-autoscaler/pkg/proxy"
	"github.com/greenkeytech/zero-pod-autoscaler/pkg/scaler"
)

func ListenAndProxy(ctx context.Context, addr, target string, sc *scaler.Scaler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("failed to accept connection: %w", err)
		}

		log.Printf("%s->%s: accept connection", conn.RemoteAddr(), conn.LocalAddr())

		go func() {
			start := time.Now()
			defer conn.Close()

			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			err := sc.WithAvailable(ctx, func() error {
				// race condition here: could now be
				// unavailable, but not much we can do
				return proxy.ProxyTo(conn, target)
			})
			if err != nil {
				log.Printf("%s->%s: failed to proxy: %v", conn.RemoteAddr(), conn.LocalAddr(), err)
			}

			log.Printf("%s->%s: close connection %s",
				conn.RemoteAddr(), conn.LocalAddr(), time.Since(start))
		}()
	}
}

func makeListOptions(name, labelSelector string) metav1.ListOptions {
	options := metav1.ListOptions{}
	if name != "" {
		options.FieldSelector = fmt.Sprintf("metadata.name=%s", name)
	}
	options.LabelSelector = labelSelector
	return options
}

func main() {
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	loadingRules, configOverrides := kubeconfig.BindKubeFlags(flags)

	addr := flags.String("address", "localhost:8080", "listen address")
	deployment := flags.String("deployment", "", "name of Deployment to scale")
	labelSelector := flags.StringP("selector", "l", "", "label selector of Deployment")
	ep := flags.String("endpoints", "", "name of Endpoints to watch for ready addresses")
	target := flags.String("target", "", "target address to which to proxy requests")
	ttl := flags.Duration("ttl", 1*time.Hour, "idle duration before scaling to zero")

	if err := flags.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}

	// the kube flags bound above include a --namespace flag...
	namespace := "default"
	if configOverrides.Context.Namespace != "" {
		namespace = configOverrides.Context.Namespace
	}

	clientset, err := kubeconfig.BuildClientset(loadingRules, configOverrides)
	if err != nil {
		log.Fatalf("error building kubernetes clientset: %v", err)
	}

	ctx := context.Background()

	sc, err := scaler.New(ctx, clientset, namespace,
		makeListOptions(*deployment, *labelSelector),
		makeListOptions(*ep, *labelSelector),
		*target,
		*ttl)
	if err != nil {
		log.Fatal(err)
	}

	if err := ListenAndProxy(ctx, *addr, *target, sc); err != nil {
		log.Fatal(err)
	}
}
