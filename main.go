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
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	// required to work with clientcmd/api
	flag "github.com/spf13/pflag"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/greenkeytech/zero-pod-autoscaler/pkg/kubeconfig"
	"github.com/greenkeytech/zero-pod-autoscaler/pkg/proxy"
	"github.com/greenkeytech/zero-pod-autoscaler/pkg/scaler"
)

// global health status
var globalHealth = int32(1)

func Healthy() bool {
	return atomic.LoadInt32(&globalHealth) != 0
}

func SetUnhealthy() {
	if atomic.CompareAndSwapInt32(&globalHealth, 1, 0) {
		log.Printf("system is unhealthy")
	}
}

func SetHealthy() {
	if atomic.CompareAndSwapInt32(&globalHealth, 0, 1) {
		log.Printf("system is healthy")
	}
}

type acceptResult struct {
	conn net.Conn
	err  error
}

func Iterate(ctx context.Context, accepts chan acceptResult, wg sync.WaitGroup, target string, sc *scaler.Scaler) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case a := <-accepts:
		conn, err := a.conn, a.err
		if err != nil {
			return fmt.Errorf("failed to accept connection: %w", err)
		}

		log.Printf("%s->%s: accept connection", conn.RemoteAddr(), conn.LocalAddr())

		wg.Add(1)
		go func() {
			start := time.Now()

			defer wg.Done()
			defer conn.Close()

			err := sc.UseConnection(func() error {
				// race condition here: could become
				// unavailable immediately after
				// reporting available, but not much
				// we can do

				select {
				case <-sc.Available():
					return proxy.ProxyTo(conn, target)
				case <-time.After(0):
					// was not immediately available; continue below
				}

				log.Printf("%s->%s: waiting for upstream to become available",
					conn.RemoteAddr(), conn.LocalAddr())
				select {
				case <-sc.Available():
					log.Printf("%s->%s: upstream available after %s",
						conn.RemoteAddr(), conn.LocalAddr(),
						time.Since(start))
					return proxy.ProxyTo(conn, target)
				case <-time.After(5 * time.Minute):
					return fmt.Errorf("timed out waiting for available upstream")
				}
			})
			if err != nil {
				log.Printf("%s->%s: failed to proxy: %v", conn.RemoteAddr(), conn.LocalAddr(), err)
				SetUnhealthy()
			} else {
				SetHealthy()
			}

			log.Printf("%s->%s: close connection after %s",
				conn.RemoteAddr(), conn.LocalAddr(), time.Since(start))
		}()

		return nil
	}
}

func ListenAndProxy(ctx context.Context, addr, target string, sc *scaler.Scaler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on `%s`: %v", err)
	}
	log.Printf("proxy listening on %s", addr)

	accepts := make(chan acceptResult)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(0):
				// proceed
			}

			conn, err := ln.Accept()
			accepts <- acceptResult{conn, err}
		}
	}()

	wg := sync.WaitGroup{}

	for {
		err := Iterate(ctx, accepts, wg, target, sc)
		if err != nil {
			log.Printf("refusing new connections")
			ln.Close()
			close(stop)
			break
		}
	}

	log.Printf("draining existing connections")
	wg.Wait()
	return nil
}

func makeListOptions(name, labelSelector string) metav1.ListOptions {
	options := metav1.ListOptions{}
	if name != "" {
		options.FieldSelector = fmt.Sprintf("metadata.name=%s", name)
	}
	options.LabelSelector = labelSelector
	return options
}

func initializeSignalHandlers(cancel func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		log.Printf("received signal: %s", <-c)
		cancel()
	}()
}

func runAdminServer(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on `%s`: %v", addr, err)
	}

	log.Printf("admin server listening on %s", addr)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(resp http.ResponseWriter, req *http.Request) {
		if !Healthy() {
			resp.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		resp.WriteHeader(http.StatusOK)
	})

	s := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() {
		if err := s.Serve(ln); err != nil {
			log.Printf("admin server exited: %v", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	return s.Shutdown(shutdownCtx)
}

func main() {
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	loadingRules, configOverrides := kubeconfig.BindKubeFlags(flags)

	addr := flags.String("address", "localhost:3000", "listen address")
	adminAddr := flags.String("admin-address", "localhost:8080", "listen address of http admin interface")
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

	ctx, cancel := context.WithCancel(context.Background())
	initializeSignalHandlers(cancel)

	wg := sync.WaitGroup{}

	////////////////////////////////////////////////////////////
	// admin server
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := runAdminServer(ctx, *adminAddr)
		if err != nil {
			log.Printf("admin server exited: %v")
		}
	}()

	////////////////////////////////////////////////////////////
	// scaler
	sc, err := scaler.New(context.Background(), clientset, namespace,
		makeListOptions(*deployment, *labelSelector),
		makeListOptions(*ep, *labelSelector),
		*target,
		*ttl)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		err := sc.Run(context.Background())
		if err != nil {
			log.Printf("scaler exited: %v", err)
		}
	}()

	////////////////////////////////////////////////////////////
	// proxy server
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := ListenAndProxy(ctx, *addr, *target, sc)
		if err != nil {
			log.Printf("proxy exited: %v", err)
		}
	}()

	wg.Wait()
	log.Printf("exiting")
}
