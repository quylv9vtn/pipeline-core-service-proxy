# pipeline-core-service

Regional routing gateway in front of the per-region Kubeflow Pipelines apiservers.
It exposes the same API surface as the regional apiserver (KFP v2beta1 REST +
Viettel extensions: `/apis/v2beta1/projects`, `/quota/*`, run tasks/log/artifact
endpoints), requires a **region** on every request, and forwards each request —
byte-identical, minus the region markers — to that region's apiserver.

Deployed once in a central cluster; routes cross-cluster to each region's endpoint
(today NodePort-style, e.g. `http://10.24.10.26:31132` for `vn-north-1`).

## Region contract (strict)

Every proxied request must carry a region. Precedence:

1. `X-Region: vn-north-1` header
2. `?region=vn-north-1` query parameter
3. top-level `"region"` field in a JSON request body

The region is **stripped before forwarding**:

- the query parameter is removed (order/encoding of other params preserved),
- the top-level JSON `region` key is removed (nested `region` keys and number
  literals are preserved byte-identical; `Content-Length` is recomputed).

Stripping is mandatory: the upstream apiserver unmarshals JSON with
`DiscardUnknown: false` and would reject an unknown `region` field itself.

The `X-Region` header is **forwarded** to the region (audit trail; the apiserver
ignores unknown headers).

**Multipart uploads** (`POST /apis/v2beta1/pipelines/upload`, `upload_version`)
never have their body parsed — use the header or query parameter there.

Errors (JSON):

| Case | Status | Body |
|---|---|---|
| region missing | 400 | `{"error":"region_required","valid_regions":[...],"hint":"..."}` |
| unknown region | 400 | `{"error":"unknown_region","message":"Unknown region \"x\".","valid_regions":[...]}` |
| body `region` not a string | 400 | `{"error":"invalid_region_field"}` |
| multipart without header/query region | 400 | `{"error":"region_required_for_multipart"}` |
| JSON body over `MAX_JSON_BODY_BYTES` | 413 | `{"error":"payload_too_large"}` |
| region upstream unreachable | 502 | `{"error":"upstream_unavailable","region":"..."}` (no retry — a gateway must never replay a possibly-applied POST) |

> **`region` is a reserved top-level key** in every proxied JSON body. A future
> upstream API must not define a top-level `region` field with another meaning.

## Gateway-owned endpoints (no region required)

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | gateway liveness (k8s probes; never depends on upstream health) |
| `GET /regions` | configured regions, endpoints, last probed health |
| `GET /metrics` | Prometheus (`core_gateway_requests_total{region,method,code}`, `..._request_duration_seconds`, `..._upstream_errors_total`, `..._region_healthy`) |
| `GET /swagger` | Swagger UI; spec at `/swagger/swagger.json` and `/swagger/v2beta1/swagger.json` with the `region` parameter injected on every operation |

`/` redirects to `/swagger`.

## Configuration (env)

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | listen port |
| `LOG_LEVEL` | `info` | `debug` raises glog verbosity |
| `REGIONS_CONFIG_PATH` | `/etc/core-gateway/regions.json` | regions file (ConfigMap-mounted) |
| `MAX_JSON_BODY_BYTES` | `33554432` (32 MiB) | gateway JSON buffering cap (413 beyond) |
| `HEALTH_CHECK_INTERVAL_SECONDS` | `30` | region prober period; `<=0` disables |
| `SWAGGER_SOURCE_REGION` | first region (sorted) | region to fetch the live spec from |
| `SWAGGER_FETCH_TIMEOUT_SECONDS` | `10` | live-spec fetch timeout |

### regions.json

```json
{
  "regions": {
    "vn-north-1": { "endpoint": "http://10.24.10.26:31132" }
  }
}
```

The file is hot-reloaded every 30s (poll on mtime/size — kubelet swaps
ConfigMap symlinks, which file-watchers miss). Invalid content is rejected and
the previous snapshot keeps serving.

The spec served on `/swagger` is fetched from a region at startup (with a
30s background retry until reachable) from the checked-in fallback copy of
`kfp_api_single_file.swagger.json`, then refreshed periodically.

## Local run

```powershell
$env:REGIONS_CONFIG_PATH = "regions.local.json"
$env:PORT = "8080"
go run . 
```

Smoke test (real region):

```bash
curl -s http://localhost:8080/regions
curl -s -H "X-Region: vn-north-1" "http://localhost:8080/apis/v2beta1/pipelines?page_size=5"
curl -s "http://localhost:8080/apis/v2beta1/pipelines?region=vn-north-1&page_size=5"
curl -s -X POST -H "Content-Type: application/json" \
  -d '{"display_name":"gw-smoke","region":"vn-north-1"}' \
  http://localhost:8080/apis/v2beta1/experiments
curl -s -i "http://localhost:8080/apis/v2beta1/pipelines"            # -> 400 region_required
curl -s -i -H "X-Region: vn-south-9" http://localhost:8080/apis/v2beta1/pipelines  # -> 400 unknown_region
curl -s -F "uploadfile=@../pipeline-sample-multistep-cpu.yaml" \
  "http://localhost:8080/apis/v2beta1/pipelines/upload?region=vn-north-1&name=gw-smoke"
```

## Build & deploy

```powershell
make test
docker build -t harbor-local.aiplatformdev.freeddns.org/ai-platform/pipeline/pipeline-core-service:20260911-initial .
docker push harbor-local.aiplatformdev.freeddns.org/ai-platform/pipeline/pipeline-core-service:20260911-initial

kubectl kustomize deploy/kustomize/overlays/central   # inspect
kubectl apply -k deploy/kustomize/overlays/central    # or the ArgoCD Application in deploy/argocd/
```

Update the region table by editing the `core-gateway-regions` ConfigMap —
no pod restart needed.

## Project layout

```
main.go                     wiring: config → store/dispatcher → router → server
internal/config/            env config (viper) + regions.json validation + atomic Store
internal/region/            strict extract/validate/strip (header > query > JSON body)
internal/middleware/        Region (validation + metrics), Logging, Recovery
internal/proxy/             per-region ReverseProxy Dispatcher + health prober
internal/handlers/          /healthz, /regions
internal/swagger/           UI + spec loader/transform (region param injection)
internal/metrics/           Prometheus series
deploy/kustomize/           base + central overlay (NodePort)
deploy/argocd/              example ArgoCD Application
```
