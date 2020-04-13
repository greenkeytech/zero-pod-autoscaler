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
package kubeconfig

import (
	// required to work with clientcmd/api
	flag "github.com/spf13/pflag"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func BindKubeFlags(flags *flag.FlagSet) (*clientcmd.ClientConfigLoadingRules, *clientcmd.ConfigOverrides) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	flags.StringVar(&loadingRules.ExplicitPath, "kubeconfig", "", "Path to kubeconfig file to use")

	configOverrides := clientcmd.ConfigOverrides{}
	clientcmd.BindOverrideFlags(&configOverrides, flags, clientcmd.RecommendedConfigOverrideFlags(""))

	return loadingRules, &configOverrides
}

// BuildClientset builds a config that should work both in-cluster
// with no args and out-of-cluster with appropriate args and returns a
// clientset.
func BuildClientset(loadingRules *clientcmd.ClientConfigLoadingRules, configOverrides *clientcmd.ConfigOverrides) (*kubernetes.Clientset, error) {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}
