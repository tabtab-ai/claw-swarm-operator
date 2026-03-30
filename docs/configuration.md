# Configuration

All configuration is done via Helm values. Run `helm show values charts/claw-swarm-operator` to see the full defaults.

## Operator Settings (`operatorConfig`)

These are the core settings that control the operator's behavior.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `operatorConfig.ingressDomain` | `example.com` | Base domain for per-instance Ingress hostnames |
| `operatorConfig.ingressClassName` | `nginx-ingress` | Ingress controller class (e.g. `nginx`, `kong`) |
| `operatorConfig.ingressHostPrefix` | `claw` | Subdomain prefix: `{prefix}-{name}.{domain}` |
| `operatorConfig.ingressTLSSecret` | `""` | TLS secret name for Ingress; empty = no TLS |
| `operatorConfig.poolSize` | `2` | Number of idle instances to maintain |
| `operatorConfig.runtimeImage` | `ghcr.io/openclaw/openclaw:latest` | OpenClaw container image for managed instances |
| `operatorConfig.storageClass` | `standard` | StorageClass for instance PVCs |
| `operatorConfig.requestCPU` | `1` | CPU request per instance |
| `operatorConfig.limitCPU` | `2` | CPU limit per instance |
| `operatorConfig.requestMemory` | `2Gi` | Memory request per instance |
| `operatorConfig.limitMemory` | `4Gi` | Memory limit per instance |
| `operatorConfig.imagePullSecrets` | `""` | Comma-separated imagePullSecret names for instances |
| `operatorConfig.nodeSelector` | `""` | Node label value for instance pod placement |

## Operator Deployment

Standard Helm values controlling the operator's own deployment.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `replicaCount` | `1` | Number of operator replicas |
| `image.repository` | `openclaw/claw-swarm-operator` | Operator image repository |
| `image.tag` | _(chart appVersion)_ | Operator image tag |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `imagePullSecrets` | `[]` | Pull secrets for the operator image |
| `resources` | `{}` | CPU/memory requests and limits for the operator pod |
| `nodeSelector` | `{}` | Node selector for the operator pod |
| `tolerations` | `[]` | Tolerations for the operator pod |
| `affinity` | `{}` | Affinity rules for the operator pod |

## Service Account

| Parameter | Default | Description |
|-----------|---------|-------------|
| `serviceAccount.create` | `true` | Create a ServiceAccount for the operator |
| `serviceAccount.name` | `""` | ServiceAccount name; auto-generated if empty |
| `serviceAccount.annotations` | `{}` | Annotations to add to the ServiceAccount |

## Ingress / HTTPRoute (Operator Metrics)

These control external access to the operator's own metrics/health endpoints — not the managed instances.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `ingress.enabled` | `false` | Enable Ingress for the operator |
| `ingress.className` | `""` | Ingress class |
| `ingress.hosts` | _(see values.yaml)_ | Ingress host rules |
| `httpRoute.enabled` | `false` | Enable Gateway API HTTPRoute instead of Ingress |
| `httpRoute.parentRefs` | _(see values.yaml)_ | Gateway references for HTTPRoute |

