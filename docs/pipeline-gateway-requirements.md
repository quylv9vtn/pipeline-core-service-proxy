# Pipeline Core Gateway – Yêu cầu tích hợp CMP Pipeline service

Tài liệu này định nghĩa những gì **pipeline-core-service (gateway)** cần bổ sung
để trở thành điểm truy cập duy nhất của FE cho AI Pipeline service, khi CMP đã
bỏ toàn bộ pipeline entity models (chỉ giữ `PipelineRegion` + quota governance).

> Phân vai: yêu cầu cho **regional KFP apiserver** (formerly "core service")
> nằm ở `cmp-backend/backend/ai_pipeline/docs/core-pipeline-requirements.md`.
> Tài liệu này chỉ bao phủ lớp **gateway**.

---

## 1. Kiến trúc mục tiêu

```
FE ──► Enforcer Proxy (X-CMP-* assertion) ──► Core Gateway (service này)
                                                  │
                              ┌───────────────────┼───────────────────┐
                              ▼                   ▼                   ▼
                        KFP vn-north-1      KFP vn-north-2        KFP ...
                   http://10.24.1.11:31132      ...

CMP ──(service activation)──► Gateway: provisioning API
CMP ──(quota limits / usage sync)──► Gateway ──route──► regional KFP /quota/*
```

Hiện trạng gateway (đã có):

- Regional routing theo thứ tự ưu tiên: `X-Region` header > `?region=` query >
  top-level JSON body `region` field (strip trước khi forward)
- Hot-reload `regions.json` (region name → endpoint), health probe per-region
- Byte-faithful reverse proxy, Prometheus metrics, Swagger UI với region param
- **Chưa có**: auth, DB, namespace resolution, flavor/image catalog, provisioning

---

## 2. Nhận xét về thiết kế đề xuất

| Đề xuất | Đánh giá |
|---|---|
| `region_id` param trong List API, body field trong Create API | Đã khớp với cơ chế extract hiện tại. **Nhưng**: gateway route theo **region name** (`vn-north-1`), không phải UUID. Quyết định: dùng region name làm key duy nhất (CMP `Region.name` trùng tên), FE gửi name — không cần bảng mapping UUID→name |
| `Project-ID` header → namespace | Đúng, nhưng phải **cross-check với enforcer assertion** (X-CMP-Project-ID) để tránh IDOR: FE gửi Project-ID khác project đã được authorize là lỗi 403 |
| project-region-namespace insert khi CMP activate | Đúng, nhưng **chưa đủ**: K8s namespace phải tồn tại trong cluster regional. Gateway phải fan-out: insert DB row **và** gọi regional KFP tạo namespace |
| Flavor/Image để ở gateway | Đúng — KFP upstream không có khái niệm này, gateway là nơi hợp lý |
| Enforcer integration | Bắt buộc bổ sung — hiện gateway không có auth nào, bất kỳ ai reach được đều gọi được mọi region |
| Quota | **Giữ nguyên ở regional KFP** (enforcement atomic với run creation). Gateway chỉ route `/quota/*` như hiện tại |

---

## 3. Database (mới)

Gateway hiện stateless — cần thêm DB (PostgreSQL) với các bảng:

### 3.1. `project_regions` — project-region-namespace mapping

```sql
CREATE TABLE project_regions (
    project_id   UUID        NOT NULL,   -- CMP project UUID (từ Project-ID header)
    region_name  TEXT        NOT NULL,   -- 'vn-north-1' (khớp regions.json key)
    namespace    TEXT        NOT NULL,   -- '{project_slug}-pipeline' (≤63 ký tự)
    status       TEXT        NOT NULL,   -- 'provisioning' | 'ready' | 'failed'
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, region_name)
);
```

- Insert khi CMP gọi provisioning API (mục 5) — CMP là **nguồn duy nhất** ghi bảng này
- Lookup path nóng của mọi request project-scoped → cần cache in-memory TTL (~60s)

### 3.2. `flavors` và `images` — resource catalog (chuyển từ CMP)

```sql
CREATE TABLE flavors (
    id           UUID PRIMARY KEY,
    region_name  TEXT NOT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    cpu          INT  NOT NULL CHECK (cpu >= 1),
    memory_gib   INT  NOT NULL CHECK (memory_gib >= 1),
    gpu_model    TEXT,          -- nullable; 'NVIDIA-GeForce-RTX-2080', 'NVIDIA-H200', ...
    gpu_count    INT  NOT NULL DEFAULT 0,
    status       TEXT NOT NULL DEFAULT 'enabled',  -- 'enabled' | 'disabled'
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (region_name, name)
);

CREATE TABLE images (
    id           UUID PRIMARY KEY,
    region_name  TEXT NOT NULL,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    version      TEXT NOT NULL,
    image_url    TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'enabled',
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (region_name, name, version)
);
```

- Ràng buộc kế thừa từ CMP model cũ: `gpu_model` set thì `gpu_count > 0` và ngược lại
- Quản lý catalog: admin CRUD (ops) hoặc CMP push khi ops cập nhật — chọn 1,
  tránh 2 đường ghi song song

---

## 4. Region routing – điều chỉnh

Đã có sẵn: extract theo header/query/body, strip, hot-reload, health probe.

Bổ sung:

| # | Yêu cầu |
|---|---|
| 4.1 | Key chuẩn hóa là **region name** (VD `vn-north-1`) — khớp `regions.json` và CMP `Region.name`. Không nhận region UUID |
| 4.2 | **Multipart requests** (create pipeline / pipeline version): hiện gateway trả 400 `region_required_for_multipart` khi region nằm trong JSON body. Giữ nguyên behavior — FE phải gửi `X-Region` header cho multipart. Ghi rõ trong Swagger |
| 4.3 | `GET /regions` đang lộ endpoint nội bộ — giới hạn cho internal network hoặc bỏ `endpoint` field khi phục vụ FE |

---

## 5. Provisioning API (gọi bởi CMP khi activate service)

CMP hiện gọi `pipeline_project_api.get_or_create(project, region)` với
`{project_id, name: project.slug, namespace: '{slug}-pipeline'}`. Endpoint này
giữ nguyên contract, nhưng giờ là **gateway-owned**:

```
PUT /apis/v2beta1/projects
{
  "project_id": "<uuid>",
  "name": "<project slug>",
  "region": "vn-north-1",
  "namespace": "<slug>-pipeline"
}
```

| # | Yêu cầu |
|---|---|
| 5.1 | **Idempotent** (get_or_create semantics): gọi lại với cùng payload → 200, không duplicate row |
| 5.2 | Fan-out: insert/update `project_regions` row (status `provisioning`) **và** gọi regional KFP đảm bảo namespace tồn tại trong cluster region đó. Thành công → status `ready`. Failure regional → giữ `provisioning`, retry nền |
| 5.3 | Auth: request phải đi qua service-key hoặc mTLS (chỉ CMP gọi, không phải FE) |
| 5.4 | Namespace validation: `{slug}-pipeline`, ≤ 63 ký tự, RFC 1123 label — reject trước khi ghi DB |
| 5.5 | Khi CMP deactivate service: gọi DELETE `/apis/v2beta1/projects/{project_id}?region=` — gateway đánh dấu/ xóa mapping (việc dọn resource trong namespace là trách nhiệm regional KFP) |

---

## 6. Namespace resolution middleware (mới)

Mọi request project-scoped từ FE phải resolve `(Project-ID, region)` → namespace
trước khi forward:

| # | Yêu cầu |
|---|---|
| 6.1 | Đọc `Project-ID` header — bắt buộc với mọi endpoint project-scoped (pipelines, versions, runs, experiments, recurring runs). Thiếu → 400 |
| 6.2 | **Cross-check** `Project-ID` header với `X-CMP-Project-ID` từ enforcer assertion. Không khớp → 403 |
| 6.3 | Lookup `(project_id, region_name)` trong `project_regions` (có cache). Không tìm thấy hoặc status != `ready` → **412** với error code `project_not_provisioned` — FE hiểu là service chưa được activate |
| 6.4 | Inject namespace vào proxied request theo cơ chế regional KFP kỳ vọng — KFP multi-user mode dùng namespaced path (`/apis/v2beta1/namespaces/{namespace}/runs`) hoặc `Kubeflow-UserId` header. **Cần chốt với team KFP**: chọn 1 cơ chế, nhất quán cả 2 phía |
| 6.5 | List endpoints: đảm bảo kết quả chỉ gồm resource của namespace đó (inject namespace filter vào query/path). Gateway không được để FE thấy resource cross-project |
| 6.6 | Strip `Project-ID` header trước khi forward (upstream không cần) |

---

## 7. Enforcer integration (mới)

FE không được gọi gateway trực tiếp — mọi request đi qua enforcer, gateway nhận
request đã được CMP authorize với `X-CMP-*` assertion headers.

| # | Yêu cầu |
|---|---|
| 7.1 | Middleware xác thực caller: presence + validity của `X-CMP-*` headers (ít nhất `X-CMP-User-GID`, `X-CMP-Project-ID`). Thiếu/sai → 401 |
| 7.2 | Trust boundary: chỉ tin X-CMP-* khi request đến từ enforcer — tối thiểu là network policy (chỉ enforcer reach được gateway), tốt hơn là verify signature/mTLS. Chốt mức nào với platform team |
| 7.3 | Forward identity xuống regional KFP nếu KFP cần audit (`Kubeflow-UserId` = user GID) |
| 7.4 | Per-endpoint action check: CMP `/internal/authz` trả theo `action_urns` (VD `urn:action:ai_pipeline:run:create`). Enforcer chịu trách nhiệm chính; gateway chỉ cần assertion hợp lệ, **không** gọi lại CMP per-request |

---

## 8. Flavor/Image endpoints (mới)

Thay thế 2 endpoints CMP đã bỏ:

```
GET /apis/v2beta1/flavors?region=vn-north-1        → enabled flavors của region
GET /apis/v2beta1/images?region=vn-north-1         → enabled images của region
```

| # | Yêu cầu |
|---|---|
| 8.1 | Trả đúng shape CMP từng trả (id, name, description, region, cpu, memory_gib, gpu_model, gpu_count, status, created_at, updated_at) để FE không phải đổi contract |
| 8.2 | Chỉ trả `status=enabled` cho FE; admin API trả hết |
| 8.3 | Không cần `Project-ID` — catalog là region-scoped, không project-scoped |

---

## 9. Validation ownership (phân tầng validate)

Nguyên tắc: **mỗi tầng chỉ validate những gì nó sở hữu**. Gateway không
validate thay phần regional KFP — tránh drift logic và false confidence.

| Validation | Gateway | Regional KFP |
|---|---|---|
| Region tồn tại/healthy, routing | ✅ (đã có) | |
| Enforcer assertion, Project-ID khớp `X-CMP-Project-ID` | ✅ (§6, §7) | |
| Project đã provision trong region (namespace lookup) | ✅ (§6) | |
| `flavor_id`/`image_id` tồn tại + enabled trong region | ✅ | |
| Resolve steps vào spec YAML | ✅ (§9.1) | |
| Cấu trúc YAML spec, `resource_envelope` format | | ✅ |
| `pipeline_version` tồn tại + READY, cùng project | | ✅ (atomic) |
| `experiment` tồn tại, cùng project | | ✅ (atomic) |
| Run parameters khớp parameter definitions trong spec | | ✅ |
| Cron 5-field, schedule XOR, `max_concurrency` range | | ✅ |
| Deletion safety (còn Run/Job tham chiếu) | | ✅ |
| Quota enforce | | ✅ |

### 9.1. Ngoại lệ body-mutation: resolve steps trong spec

Với `POST /pipelines/` và `POST /pipeline-versions/` (multipart YAML + steps):

1. Gateway lookup `flavor_id`/`image_id` trong catalog theo region của request
2. Thiếu hoặc disabled → 400/404 trước khi request chạm upstream
3. Hợp lệ → resolve thành `image_url` + resources cụ thể, rewrite spec YAML,
   forward

Đây là **điểm duy nhất** gateway sửa request body (phá vỡ nguyên tắc
byte-faithful proxy) — giữ scope đúng 2 endpoints này, mọi endpoint khác vẫn
pass-through.

### 9.2. Quan hệ entity nằm hoàn toàn ở regional KFP

- KFP là source of truth duy nhất; FK check phải **atomic với write** (cùng
  transaction) để tránh TOCTOU — version bị xóa giữa lúc check và lúc tạo Run
- Cross-region consistency tự đảm bảo: mỗi request route vào đúng 1 region,
  mọi entity liên quan cùng sống trong 1 KFP. FE gửi sai region → KFP trả 404
  "version not found" — behavior đúng, owner báo lỗi

---

## 10. Quota routing (không đổi vai trò)

| # | Yêu cầu |
|---|---|
| 9.1 | `/quota/*` tiếp tục là pass-through tới regional KFP (enforcement nằm ở đó, atomic với run creation — xem `core-quota-requirements.md`) |
| 9.2 | CMP gọi `PUT /apis/v2beta1/quota/limits` và `POST /apis/v2beta1/quota/usage/bulk` qua gateway với region xác định (header/query). Đây là service-to-service: cho phép đi qua enforcer bằng service-key hoặc route internal trực tiếp |
| 9.3 | Namespace resolution **không** áp dụng cho `/quota/*` (CMP thao tác theo project_id trong body, không phải Project-ID header) |

---

## 11. Regional KFP apiserver – yêu cầu chi tiết

Tách thành tài liệu riêng: `docs/regional-kfp-requirements.md`. Tóm tắt:

- Spec validation (cấu trúc YAML, `resource_envelope`) + trả envelope trong GET
- Entity CRUD + state machine chuẩn (Run/Job/Experiment/Pipeline/Version)
- Relational validation atomic (version READY, experiment tồn tại, cùng project)
- Deletion safety + cascade flag
- Quota: `PUT /quota/limits`, enforce atomic, `POST /quota/usage/bulk`, tự release
- Namespace provisioning trong cluster (gateway gọi khi provisioning fan-out)

Không còn trách nhiệm (so với thiết kế cũ): auth (enforcer + gateway lo),
region handling (gateway strip), flavor/image catalog (gateway resolve sẵn),
job→runs discovery cho CMP sync (CMP đã bỏ Run model).

---

## 12. Deployment & vận hành

| # | Yêu cầu |
|---|---|
| 11.1 | Thêm DB vào deployment: migration strategy, connection pool, secret quản lý DSN |
| 11.2 | HA: hiện `replicas: 1` — với DB state, chạy ≥ 2 replicas + PodDisruptionBudget. `regions.json` hot-reload giữ nguyên (không đưa endpoints vào DB) |
| 11.3 | Metrics: thêm series cho namespace resolution (hit/miss/latency), provisioning fan-out failures, enforcer auth rejects |
| 11.4 | Swagger: cập nhật spec — thêm `Project-ID` header (required), flavors/images/quota paths, `x-region-precedence` giữ nguyên |

---

## 13. Checklist tóm tắt

```
DB & data
  ☐ PostgreSQL + migration tooling
  ☐ Bảng project_regions (project_id, region_name, namespace, status)
  ☐ Bảng flavors, images (region-scoped catalog, kế thừa shape CMP cũ)
  ☐ Cache namespace lookup (TTL ~60s)

Routing (điều chỉnh nhẹ)
  ☐ Chốt region key = region name; Swagger ghi rõ multipart phải dùng X-Region header
  ☐ /regions endpoint: ẩn endpoint nội bộ hoặc giới hạn internal

Provisioning API
  ☐ PUT /apis/v2beta1/projects — idempotent, service-key auth
  ☐ Fan-out tạo namespace ở regional KFP, status tracking
  ☐ DELETE cho deactivate

Namespace middleware
  ☐ Project-ID header bắt buộc + cross-check X-CMP-Project-ID
  ☐ 412 project_not_provisioned khi chưa activate
  ☐ Inject namespace (path rewrite hoặc UserId header — chốt với KFP team)
  ☐ Namespace filter cho mọi list endpoint project-scoped

Enforcer
  ☐ Middleware reject request thiếu/sai X-CMP-* assertion (401)
  ☐ Trust boundary: network policy tối thiểu, mTLS/signature nếu có
  ☐ Forward identity xuống KFP cho audit

Flavor/Image
  ☐ GET /flavors, GET /images (region-scoped, enabled-only cho FE)
  ☐ Admin CRUD hoặc CMP push — chọn 1 đường ghi

Validation (mục 9)
  ☐ Resolve steps (flavor/image) trong spec trước khi forward — chỉ 2 endpoints multipart
  ☐ Không validate trùng phần regional KFP sở hữu (YAML structure, entity relations, quota)

Quota
  ☐ Pass-through giữ nguyên; CMP service-to-service path xác định

Ops
  ☐ ≥2 replicas + PDB; DB secret/migration
  ☐ Metrics: ns-resolution, provisioning, auth rejects
  ☐ Swagger cập nhật Project-ID + paths mới
```
