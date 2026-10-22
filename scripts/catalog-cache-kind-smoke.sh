#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
service_dir="$repo_root/src/productcatalogservice"
work_dir="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/p24-catalog-kind-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
tool_dir="$work_dir/tools"
cluster="p24-catalog-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
namespace="p24-catalog"
image="cached-catalog-microservices/productcatalogservice:p24"
kind_version="v0.33.0"
kubectl_version="v1.35.2"
node_image="kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0"
port_forward_pid=""
cluster_attempted=0
evidence_dir="$repo_root/reports/evidence/kind-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
mkdir -p "$tool_dir"
mkdir -p "$evidence_dir"

cleanup() {
  status=$?
  trap - EXIT
  if [[ -n "$port_forward_pid" ]]; then
    kill "$port_forward_pid" 2>/dev/null || true
    wait "$port_forward_pid" 2>/dev/null || true
  fi
  if [[ "$cluster_attempted" == 1 ]] && "$tool_dir/kind" get clusters 2>/dev/null | grep -Fxq "$cluster"; then
    {
      echo "Cluster: $cluster"
      echo "Smoke exit status before cleanup: $status"
      echo "Node image: $node_image"
      echo
      kubectl get nodes -o wide || true
      echo
      kubectl get storageclass -o yaml || true
      echo
      kubectl -n "$namespace" get pods,services,pvc,networkpolicy,statefulsets,deployments -o wide || true
      echo
      kubectl -n "$namespace" get events --sort-by=.lastTimestamp || true
      echo
      kubectl -n "$namespace" describe pods,pvc,statefulsets,deployments || true
      echo
      kubectl -n "$namespace" logs -l app=catalogdbservice --all-containers=true --prefix=true || true
      echo
      kubectl -n "$namespace" logs -l app=productcatalogservice --all-containers=true --prefix=true || true
    } >"$evidence_dir/cluster-diagnostics.txt" 2>&1
    "$tool_dir/kind" delete cluster --name "$cluster" || status=1
  fi
  printf 'exit_status=%s\n' "$status" >"$evidence_dir/result.txt"
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    printf 'evidence_path=%s\n' "${evidence_dir#"$repo_root"/}" >>"$GITHUB_OUTPUT"
  fi
  exit "$status"
}
trap cleanup EXIT

curl --fail --location --silent --show-error \
  "https://github.com/kubernetes-sigs/kind/releases/download/${kind_version}/kind-linux-amd64" \
  --output "$tool_dir/kind-linux-amd64"
curl --fail --location --silent --show-error \
  "https://github.com/kubernetes-sigs/kind/releases/download/${kind_version}/kind-linux-amd64.sha256sum" \
  --output "$tool_dir/kind-linux-amd64.sha256sum"
(cd "$tool_dir" && sha256sum --check kind-linux-amd64.sha256sum)
mv "$tool_dir/kind-linux-amd64" "$tool_dir/kind"
chmod +x "$tool_dir/kind"

curl --fail --location --silent --show-error \
  "https://dl.k8s.io/release/${kubectl_version}/bin/linux/amd64/kubectl" \
  --output "$tool_dir/kubectl"
kubectl_sha="$(curl --fail --location --silent --show-error "https://dl.k8s.io/release/${kubectl_version}/bin/linux/amd64/kubectl.sha256")"
printf '%s  %s\n' "$kubectl_sha" "$tool_dir/kubectl" | sha256sum --check
chmod +x "$tool_dir/kubectl"
export PATH="$tool_dir:$PATH"

go -C "$service_dir" build -mod=readonly -o "$work_dir/catalogctl" ./cmd/catalogctl
docker build --tag "$image" --file "$service_dir/Dockerfile" "$service_dir"

export KUBECONFIG="$work_dir/kubeconfig"
cluster_attempted=1
kind create cluster --name "$cluster" --image "$node_image" --wait 120s
kind load docker-image "$image" --name "$cluster"

kubectl create namespace "$namespace"
kubectl kustomize "$repo_root/kustomize/overlays/catalog-cache" >"$work_dir/rendered.yaml"
python3 - "$work_dir/rendered.yaml" "$work_dir/catalog-resources.yaml" <<'PY'
import pathlib
import re
import sys

source, destination = map(pathlib.Path, sys.argv[1:])
wanted = {
    ("Deployment", "productcatalogservice"),
    ("Service", "productcatalogservice"),
    ("ServiceAccount", "productcatalogservice"),
    ("Service", "catalogdbservice"),
    ("PersistentVolumeClaim", "catalogdb-data"),
    ("StatefulSet", "catalogdbservice"),
    ("NetworkPolicy", "catalogdbservice"),
}
selected = []
for document in re.split(r"(?m)^---\s*$", source.read_text(encoding="utf-8")):
    kind = re.search(r"(?m)^kind:\s*(\S+)", document)
    name = re.search(r"(?m)^[ \t]{2}name:[ \t]*([^\r\n]+)", document)
    if kind and name and (kind.group(1), name.group(1).strip()) in wanted:
        resource = (kind.group(1), name.group(1).strip())
        selected_document = document.strip()
        selected.append(selected_document)
found = {
    (re.search(r"(?m)^kind:\s*(\S+)", document).group(1),
     re.search(r"(?m)^[ \t]{2}name:[ \t]*([^\r\n]+)", document).group(1).strip())
    for document in selected
}
if found != wanted:
    raise SystemExit(f"rendered integration resource set differs: missing={wanted-found}, extra={found-wanted}")
destination.write_text("---\n".join(selected) + "\n", encoding="utf-8")
PY
kubectl apply -n "$namespace" -f "$work_dir/catalog-resources.yaml"
kubectl -n "$namespace" wait --for=jsonpath='{.status.phase}'=Bound pvc/catalogdb-data --timeout=120s
catalog_pvc_uid="$(kubectl -n "$namespace" get pvc catalogdb-data -o jsonpath='{.metadata.uid}')"
kubectl -n "$namespace" wait --for=condition=Ready pod -l app=catalogdbservice --timeout=180s
kubectl -n "$namespace" wait --for=condition=Ready pod -l app=productcatalogservice --timeout=180s
export CATALOGCTL_GRPC_ADDR="127.0.0.1:3550"
export CATALOGCTL_ADMIN_URL="http://127.0.0.1:3552"

start_port_forward() {
  local target="$1"
  local ports="$2"
  local readiness="$3"
  kubectl -n "$namespace" port-forward "$target" $ports \
    >"$work_dir/port-forward-${target//\//-}.log" 2>&1 &
  port_forward_pid=$!
  for _ in $(seq 1 100); do
    if "$readiness"; then
      return
    fi
    sleep 0.1
  done
  cat "$work_dir/port-forward-${target//\//-}.log"
  return 1
}
gateway_ready() { curl --fail --silent "$CATALOGCTL_ADMIN_URL/admin/revision" >/dev/null; }
catalog_ready() { "$work_dir/catalogctl" list >/dev/null; }
start_port_forward deployment/productcatalogservice "3550:3550 3552:3552" gateway_ready

initial="$("$work_dir/catalogctl" list)"
product_id="$(jq -er '.products[0].id' <<<"$initial")"
product_name="$(jq -er '.products[0].name' <<<"$initial")"
[[ "$(jq -er '.products | length' <<<"$initial")" -gt 0 ]]
get_result="$("$work_dir/catalogctl" get "$product_id")"
jq -e --arg id "$product_id" '.id == $id' <<<"$get_result" >/dev/null
search_result="$("$work_dir/catalogctl" search "$product_name")"
jq -e --arg id "$product_id" 'any(.products[]; .id == $id)' <<<"$search_result" >/dev/null
benchmark="$("$work_dir/catalogctl" bench 40 8)"
jq -e '.offered == 40 and .completed == 40 and .failed == 0' <<<"$benchmark" >/dev/null

python3 - "$service_dir/products.json" "$work_dir/updated-products.json" <<'PY'
import json
import pathlib
import sys

source, destination = map(pathlib.Path, sys.argv[1:])
catalog = json.loads(source.read_text(encoding="utf-8"))
catalog["products"][0]["name"] = "P24 kind persisted catalog update"
destination.write_text(json.dumps(catalog), encoding="utf-8")
PY
revision="$("$work_dir/catalogctl" revision | jq -er '.revision')"
update_result="$("$work_dir/catalogctl" update "$work_dir/updated-products.json" "$revision")"
new_revision="$(jq -er '.revision' <<<"$update_result")"
[[ "$new_revision" -gt "$revision" ]]
updated_list="$("$work_dir/catalogctl" list)"
jq -e '.products[0].name == "P24 kind persisted catalog update"' <<<"$updated_list" >/dev/null

kubectl -n "$namespace" rollout restart statefulset/catalogdbservice
kubectl -n "$namespace" rollout status statefulset/catalogdbservice --timeout=180s
kill "$port_forward_pid" 2>/dev/null || true
wait "$port_forward_pid" 2>/dev/null || true
port_forward_pid=""
export CATALOGCTL_GRPC_ADDR="127.0.0.1:3551"
start_port_forward service/catalogdbservice "3551:3551" catalog_ready
database_after_restart=""
for _ in $(seq 1 100); do
  if database_after_restart="$("$work_dir/catalogctl" list 2>/dev/null)" && \
      jq -e '.products[0].name == "P24 kind persisted catalog update"' <<<"$database_after_restart" >/dev/null; then
    break
  fi
  database_after_restart=""
  sleep 0.1
done
[[ -n "$database_after_restart" ]]
kill "$port_forward_pid" 2>/dev/null || true
wait "$port_forward_pid" 2>/dev/null || true
port_forward_pid=""
export CATALOGCTL_GRPC_ADDR="127.0.0.1:3550"
kubectl -n "$namespace" rollout restart deployment/productcatalogservice
kubectl -n "$namespace" rollout status deployment/productcatalogservice --timeout=180s
start_port_forward deployment/productcatalogservice "3550:3550 3552:3552" gateway_ready
persisted_list="$("$work_dir/catalogctl" list)"
jq -e '.products[0].name == "P24 kind persisted catalog update"' <<<"$persisted_list" >/dev/null
persisted_revision="$("$work_dir/catalogctl" revision | jq -er '.revision')"
[[ "$persisted_revision" == "$new_revision" ]]
[[ "$(kubectl -n "$namespace" get pvc catalogdb-data -o jsonpath='{.metadata.uid}')" == "$catalog_pvc_uid" ]]
metrics="$(curl --fail --silent "$CATALOGCTL_ADMIN_URL/metrics")"
grep -q '^catalog_requests_started ' <<<"$metrics"

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "### Disposable kind catalog smoke passed"
    echo "- Cluster: $cluster using $node_image; cleanup deletes that named cluster."
    echo "- Applied only internal product-catalog, bbolt StatefulSet, default-class PVC, and database NetworkPolicy resources."
    echo "- gRPC List/Get/Search and 40-request benchmark passed; all 40 requests completed."
    echo "- Catalog update advanced revision $revision to $new_revision and was read directly from the backend after its PVC-backed pod restarted, then remained present through gateway restart."
    echo "- Loopback admin metrics exposed real catalog request counters."
  } >>"$GITHUB_STEP_SUMMARY"
fi
