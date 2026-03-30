**[English](README.md) | [中文](README_zh.md)**

<p align="center">
  <a href="https://github.com/tabtab-ai/claw-swarm-operator/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License"></a>
  <!--<a href="https://github.com/tabtab-ai/claw-swarm-operator/actions/workflows/docker-publish.yml"><img src="https://img.shields.io/github/actions/workflow/status/tabtab-ai/claw-swarm-operator/docker-publish.yml?branch=main&label=build" alt="Build Status"></a>-->
  <a href="https://hub.docker.com/r/tabtabai/claw-swarm-operator"><img src="https://img.shields.io/docker/pulls/tabtabai/claw-swarm-operator.svg" alt="Docker Pulls"></a>
  <img src="https://img.shields.io/badge/Go-1.24-00ADD8.svg" alt="Go Version">
  <img src="https://img.shields.io/badge/Kubernetes-%E2%89%A51.23-326CE5.svg" alt="Kubernetes">
</p>

# Claw Swarm Operator

一个 Kubernetes Operator，用于维护一批随时可用的 [OpenClaw](https://github.com/openclaw/openclaw) 热备实例池。

OpenClaw 实例的初始化需要时间。这个 Operator 会预先创建指定数量的空闲实例，让外部服务可以即时分配，无需等待冷启动。实例被释放后，Operator 自动补充新实例保持池满。

## 核心特性

- **零冷启动分配** — 实例提前预热，随用随取
- **完整生命周期管理** — 每个实例的 StatefulSet、Service、Ingress、Secret 自动创建和清理
- **暂停 / 恢复** — 空闲实例可挂起以节省资源，PVC 数据保留
- **定时暂停** — 设置时间注解，Operator 自动在指定时间暂停实例
- **滚动镜像更新** — 更新 `--runtime-image`，Operator 仅对空闲实例滚动更新，不影响使用中的实例

## 实例生命周期

```
  （Operator 预创建）
          │
          ▼
        空闲 ────分配────► 已占用 ────释放────► [已删除]
          ▲        │          │  ▲
          │        │        暂停  │
    （新建空闲）  （新建空闲    │  恢复
                  补充池）     ▼  │
                            已暂停
```

## 快速开始

**前置要求：** Kubernetes ≥ 1.23、Helm ≥ 3.x、集群中已安装 Ingress 控制器。

> **说明：** 如果集群中尚未安装 Ingress 控制器，可以安装 [Kong Ingress Controller](https://docs.konghq.com/kubernetes-ingress-controller/latest/)。此步骤为可选项，若集群中已有 Ingress 控制器可跳过。

```bash
helm install claw-swarm-operator charts/claw-swarm-operator \
  --namespace tabclaw \
  --create-namespace \
  --set operatorConfig.ingressDomain=claw.example.com \
  --set operatorConfig.ingressClassName=kong \
  --set operatorConfig.poolSize=5

kubectl get pods -n tabclaw
```

**创建 StatefulSet 以初始化实例池：**

```yaml
# seed.yaml

apiVersion: apps/v1
kind: StatefulSet
metadata:
  labels:
    tabtabai.com/tabclaw-init: ""
    tabtabai.com/tabclaw: ""
    tabtabai.com/tabclaw-occupied: ""
  name: start
  namespace: tabclaw
spec:
  replicas: 0
  revisionHistoryLimit: 10
  selector:
    matchLabels:
      tabtabai.com/tabclaw-init: ""
  template:
    metadata:
      labels:
        tabtabai.com/tabclaw-init: ""
    spec:
      containers:
      - image: nginx
        imagePullPolicy: Always
        name: nginx
```

```bash
kubectl apply -f seed.yaml
```

**分配实例：**
```bash
kubectl label statefulset <name> tabtabai.com/tabclaw-occupied=true -n tabclaw
```

**释放实例：**
```bash
kubectl label statefulset <name> tabtabai.com/tabclaw-occupied- -n tabclaw
```

## 工作原理

每个实例由四个 Kubernetes 资源组成，均以 StatefulSet 为 owner：

| 资源 | 说明 |
|------|------|
| **StatefulSet** | 运行 OpenClaw 网关的单 Pod |
| **Service** | ClusterIP，端口 18789（网关）+ 18790（辅助） |
| **Ingress** | `{host-prefix}-{name}.{domain}` |
| **Secret** | 该实例的网关令牌（UUIDv4） |

实例暂停（`spec.replicas=0`）时，Pod 停止，PVC 保留，Ingress 切换为保护模式屏蔽外部访问。恢复时一切还原。

## 文档

- [配置参考](docs/configuration.md) — 所有 Helm chart 参数和 operator flags 说明

**标签：**

| 标签 | 用途 |
|------|------|
| `tabtabai.com/tabclaw=true` | 标记为 Operator 管理的 StatefulSet |
| `tabtabai.com/tabclaw-name=<name>` | 实例名称 |
| `tabtabai.com/tabclaw-occupied=true` | 实例已被分配；不存在 = 空闲 |

**Annotation：**

| Annotation | 用途 |
|-----------|------|
| `tabtab.app.scheduled.deletion.time` | RFC3339 时间，到达后自动暂停实例 |

## 参与贡献

欢迎提交 Issue 和 PR！

**前置要求：** Go 1.24+、Docker、本地 Kubernetes 集群（如 [kind](https://kind.sigs.k8s.io/) 或 [minikube](https://minikube.sigs.k8s.io/)）。

```bash
git clone https://github.com/openclaw/claw-swarm-operator.git
cd claw-swarm-operator

# 修改代码

# 运行测试
make test

# 编译 operator 二进制
go build -o bin/manager ./cmd/main.go

# 本地运行（使用当前 kubeconfig 集群）
bin/manager \
  --ingress-domain=claw.example.com \
  --ingress-class-name=kong \
  --pool-size=2
```

提交较大的功能 PR 前，建议先开 Issue 讨论方案。Bug 修复和小改动可以直接提 PR。

## 常见问题

**启动后实例未被创建**

池补充是异步的，查看 Operator 日志：
```bash
kubectl logs -l app.kubernetes.io/name=claw-swarm-operator -n <namespace> -f
```

**`unable to load in-cluster configuration`**

设置 `KUBECONFIG` 环境变量，或确保 `~/.kube/config` 存在。

**释放实例后 PVC 未被删除**

`persistentVolumeClaimRetentionPolicy.whenDeleted: Delete` 需要 Kubernetes ≥ 1.23。

**实例 Pod 卡在 `Pending`**

检查节点资源是否充足，以及 StorageClass 是否有可用容量。

## License

[MIT](LICENSE)
