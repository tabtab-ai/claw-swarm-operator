/*
Copyright 2026.

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

package config

import (
	"errors"
	"flag"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	defaultPoolSize = 2
)

// Gateway defines the connection configuration for the claw gateway.
type Gateway struct {
	// Port is the port the gateway listens on. Default 18798
	Port int32
	// Auth Authentication method token or password. The default is token.
	Auth string
}

// ClawInit defines the configuration for the init container.
type ClawInit struct {
	// StorageSize is the storage capacity requested during initialization, in Kubernetes quantity format (e.g. "10Gi").
	StorageSize string
	// Env is a map of environment variables injected into the container.
	Env map[string]string

	Gateway
}

// Config defines the global runtime configuration for the Operator.
type Config struct {
	// RuntimeImage is the container image used for the claw runtime environment (e.g. ghcr.io/openclaw/openclaw:latest).
	RuntimeImage string
	// StorageClass is the Kubernetes StorageClass used for persistent volume provisioning (e.g. standard, gp2).
	StorageClass string
	// IngressDomain is the root domain for Ingress rules, combined with IngressHostPrefix to form the full hostname (e.g. example.com).
	IngressDomain string
	// IngressHostPrefix is the subdomain prefix prepended to IngressDomain (e.g. "claw" -> claw-<name>.example.com).
	IngressHostPrefix string
	// IngressClassName is the Ingress class used to select the ingress controller (e.g. nginx, traefik); leave empty to use the cluster default.
	IngressClassName string
	// IngressTlsSecret is the name of the Kubernetes Secret containing the TLS certificate and key for the Ingress; leave empty to disable TLS.
	IngressTlsSecret string
	// NodeSelectorValue is the value of the node selector label used to pin workloads to specific nodes; leave empty to allow scheduling on any node.
	NodeSelectorValue map[string]string
	// WatchNamespace is the namespace the Operator watches for resources.
	WatchNamespace string

	// Resources defines the CPU and memory requests and limits for the runtime container.
	Resources *v1.ResourceRequirements
	// PoolSize is the number of StatefulSet instances to keep available in the pool.
	PoolSize int
	// ImagePullSecrets is the list of secret names used to pull container images from private registries.
	ImagePullSecrets []string
	// Init holds the configuration for the claw.
	Init ClawInit
}

var (
	ErrMissingRuntimeImage      = errors.New("config: RuntimeImage is required")
	ErrMissingIngressDomain     = errors.New("config: IngressDomain is required")
	ErrMissingIngressHostPrefix = errors.New("config: IngressHostPrefix is required")
	ErrMissingIngressClassName  = errors.New("config: IngressClassName is required")
	ErrInvalidQuantity          = errors.New("invalid quantity")
)

func DefaultConfig() Config {
	return Config{
		RuntimeImage:      "ghcr.io/openclaw/openclaw:latest",
		StorageClass:      "",
		IngressDomain:     "",
		IngressHostPrefix: "claw",
		WatchNamespace:    "default",
		Resources: &v1.ResourceRequirements{
			Requests: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("1"),
				v1.ResourceMemory: resource.MustParse("2Gi"),
			},
			Limits: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("4Gi"),
			},
		},
		PoolSize: 2,
		Init: ClawInit{
			StorageSize: "10Gi",
			Env: map[string]string{
				"OPENCLAW_GATEWAY_BIND":  "lan",
				"OPENCLAW_WORKSPACE_DIR": "/home/node/.openclaw/workspace",
				"OPENCLAW_CONFIG_DIR":    "/home/node/.openclaw",
			},
			Gateway: Gateway{
				Port: 18789,
				Auth: "token",
			},
		},
	}
}

func (c *Config) Validate() error {
	if c.RuntimeImage == "" {
		return ErrMissingRuntimeImage
	}
	if c.IngressDomain == "" {
		return ErrMissingIngressDomain
	}
	if c.IngressHostPrefix == "" {
		return ErrMissingIngressHostPrefix
	}
	if c.IngressClassName == "" {
		return ErrMissingIngressClassName
	}
	if c.PoolSize < 0 {
		c.PoolSize = defaultPoolSize
	}

	return nil
}

type quantityFlag struct {
	list v1.ResourceList
	key  v1.ResourceName
}

func (q quantityFlag) String() string {
	if v, ok := q.list[q.key]; ok {
		return v.String()
	}
	return ""
}

func (q quantityFlag) Set(s string) error {
	v, err := resource.ParseQuantity(s)
	if err != nil {
		return ErrInvalidQuantity
	}
	q.list[q.key] = v
	return nil
}

func (c *Config) BuildFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.RuntimeImage, "runtime-image",
		c.RuntimeImage,
		"Container image used for the runtime environment (e.g. ghcr.io/openclaw/openclaw:latest)")

	fs.StringVar(&c.StorageClass, "storage-class",
		c.StorageClass,
		"Kubernetes StorageClass name used for persistent volume provisioning (e.g. standard, gp2)")

	fs.StringVar(&c.IngressDomain, "ingress-domain",
		c.IngressDomain,
		"Base domain for Ingress rules, used to construct the full hostname (e.g. example.com)")

	fs.StringVar(&c.IngressHostPrefix, "ingress-host-prefix",
		c.IngressHostPrefix,
		"Subdomain prefix prepended to the ingress domain (e.g. 'claw' -> claw-<name>.example.com)")

	fs.StringVar(&c.IngressClassName, "ingress-class-name",
		c.IngressClassName,
		"Ingress class name to select the ingress controller (e.g. nginx, traefik); leave empty to use the cluster default")

	fs.StringVar(&c.IngressTlsSecret, "ingress-tls-secret",
		c.IngressTlsSecret,
		"The name of the kubernetes.io/tls type Secret for Ingress HTTPS.",
	)

	fs.Func("node-selector",
		"Node label selectors for workload placement (e.g., 'disk=ssd,zone=us-east'). Leave empty to allow scheduling on any available node.",
		func(s string) error {
			trimmed := strings.TrimSpace(s)
			if trimmed == "" {
				// empty, don't schedule
				return nil
			}

			c.NodeSelectorValue = map[string]string{}
			for selector := range strings.SplitSeq(s, ",") {
				kv := strings.SplitN(selector, "=", 2)
				if len(kv) != 2 || kv[0] == "" {
					continue
				}
				c.NodeSelectorValue[kv[0]] = kv[1]
			}

			return nil
		},
	)

	fs.StringVar(&c.WatchNamespace, "namespace",
		c.WatchNamespace,
		"The namespace to watch",
	)

	fs.IntVar(&c.PoolSize, "pool-size",
		c.PoolSize,
		"Number of StatefulSet instances to keep available in the pool")

	fs.Func("image-pull-secrets",
		"Comma-separated list of imagePullSecret names (e.g. regcred,mysecret)",
		func(s string) error {
			if s != "" {
				c.ImagePullSecrets = strings.Split(s, ",")
			}
			return nil
		})

	fs.Var(quantityFlag{list: c.Resources.Requests, key: v1.ResourceCPU}, "request-cpu",
		"CPU request for the claw runtime container (e.g. 250m)")
	fs.Var(quantityFlag{list: c.Resources.Requests, key: v1.ResourceMemory}, "request-memory",
		"Memory request for the claw runtime container (e.g. 128Mi)")
	fs.Var(quantityFlag{list: c.Resources.Limits, key: v1.ResourceCPU}, "limit-cpu",
		"CPU limit for the claw runtime container (e.g. 500m)")
	fs.Var(quantityFlag{list: c.Resources.Limits, key: v1.ResourceMemory}, "limit-memory",
		"Memory limit for the claw runtime container (e.g. 256Mi)")
}
