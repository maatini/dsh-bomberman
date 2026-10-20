# Kubernetes deployment with Kustomize

This directory contains a Kustomize base and two overlays:

```
deploy/k8s/
├── base/                     Deployment, Service, ConfigMap generator
└── overlays/
    ├── local/                NodePort 30431, local image bomber-grid:dev
    └── prod/                 registry image placeholder, nginx Ingress + TLS
```

The app listens on **4301** inside the container. The Service exposes the same
port, so a port-forward maps it 1:1.

## Prerequisites

- `kubectl` with built-in Kustomize (v5 or newer recommended)
- a Kubernetes cluster (kind, minikube, k3d, Docker Desktop, …)
- a container runtime to build the image

## Build and load the image

```bash
docker build -t bomber-grid:dev .

# kind
kind load docker-image bomber-grid:dev

# minikube
minikube image load bomber-grid:dev
```

The Dockerfile uses a multi-stage build and produces a static Go binary in a
distroless nonroot image. The app runs with a read-only root filesystem and no
Kubernetes service-account token.

## Deploy the local overlay

```bash
kubectl apply -k deploy/k8s/overlays/local
kubectl -n bomber-grid rollout status deployment/bomber-grid
```

Access it via port-forward on the requested app port:

```bash
kubectl -n bomber-grid port-forward service/bomber-grid 4301:4301
# open http://127.0.0.1:4301
```

The local Service is also a `NodePort` on **30431**, convenient when the cluster
exposes node ports directly:

```bash
# kind example; the node IP is the container IP
kubectl get nodes -o wide
# open http://<node-ip>:30431
```

Check the deployment:

```bash
kubectl -n bomber-grid get deploy,pod,svc
kubectl -n bomber-grid logs deploy/bomber-grid
kubectl -n bomber-grid describe pod -l app.kubernetes.io/name=bomber-grid
```

Remove it:

```bash
kubectl delete -k deploy/k8s/overlays/local
```

## Render without applying

```bash
kubectl kustomize deploy/k8s/overlays/local
kubectl kustomize deploy/k8s/overlays/prod
```

or, with the standalone Kustomize CLI:

```bash
kustomize build deploy/k8s/overlays/local
```

## Production overlay

Edit these placeholders before applying:

1. **Image** in `deploy/k8s/overlays/prod/kustomization.yaml`:

   ```yaml
   images:
     - name: bomber-grid
       newName: ghcr.io/your-org/bomber-grid
       newTag: 1.2.3
   ```

   or from the overlay directory:

   ```bash
   kustomize edit set image bomber-grid=ghcr.io/your-org/bomber-grid:1.2.3
   ```

2. **Host and TLS secret** in `deploy/k8s/overlays/prod/ingress.yaml`:
   `bomber-grid.example.com` and `bomber-grid-tls`.

The nginx Ingress annotations allow long-lived WebSocket connections and disable
response buffering. If you use another ingress controller, adapt them. The
browser automatically uses `wss://` when the page is served over HTTPS.

```bash
kubectl apply -k deploy/k8s/overlays/prod
kubectl -n bomber-grid rollout status deployment/bomber-grid
```

## Why exactly one replica?

Rooms, player tokens, chat and the running game loop live in process memory.
There is no shared room store or pub/sub backplane. Two replicas would create
two independent worlds: a player could create a room on pod A and a friend could
join on pod B and never find it. Sticky sessions alone do not solve this.

Therefore:

- `replicas: 1`
- `strategy: Recreate` (a rolling update would briefly run two independent
  state stores and split traffic)
- no HorizontalPodAutoscaler

Scaling out requires shared state or room-aware routing at the edge. Until that
exists, run one replica and treat a pod restart as the end of all active rooms.

## Probes, resources and shutdown

- Readiness and liveness: `GET /healthz` on container port `http` (4301).
- Requests: 25m CPU / 48Mi memory; limits: 500m CPU / 256Mi memory.
- Graceful shutdown: the app closes WebSocket connections and stops the HTTP
  server; the pod gets 20 seconds (`terminationGracePeriodSeconds`).
- The container runs as UID/GID 65532 (distroless nonroot), drops all Linux
  capabilities, disallows privilege escalation and uses the default seccomp
  profile.
- `readOnlyRootFilesystem: true`; the server writes only to stdout/stderr.

## Image build note

The Dockerfile builds with `CGO_ENABLED=0`, producing a static binary.
`TARGETOS`/`TARGETARCH` are declared for BuildKit multi-arch builds, for example:

```bash
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=1.2.3 \
  -t ghcr.io/your-org/bomber-grid:1.2.3 --push .
```
