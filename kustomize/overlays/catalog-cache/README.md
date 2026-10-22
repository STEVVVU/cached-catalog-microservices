# Persistent catalog overlay

This internal overlay keeps the upstream Online Boutique services and points the existing `ProductCatalogService` at a one-replica bbolt backend. The backend's PVC stores the catalog database. A Kubernetes `ClusterIP` exposes its gRPC port only to `productcatalogservice` pods through the included NetworkPolicy; the admin and metrics HTTP listener remains bound to container loopback. The frontend stays a `ClusterIP`, with no Ingress or external LoadBalancer.

Build the changed catalog image from `src/productcatalogservice` and load it into a disposable kind cluster before rendering or applying this overlay:

```powershell
docker build -t cached-catalog-microservices/productcatalogservice:p24 src/productcatalogservice
kind load docker-image cached-catalog-microservices/productcatalogservice:p24 --name p24-catalog
kubectl kustomize kustomize/overlays/catalog-cache
kubectl apply -k kustomize/overlays/catalog-cache
```

The image build and manifest render can be checked without a cluster. A rendered manifest does not prove PVC attachment, pod readiness, network policy enforcement, or catalog gRPC behavior in Kubernetes. The host used for P24 has no Docker or Kubernetes CLI, so the overlay remains for a later disposable hosted kind run; no user-machine runtime or cluster was started.
