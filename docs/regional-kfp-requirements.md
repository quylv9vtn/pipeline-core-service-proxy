# Regional KFP apiserver – Yêu cầu cho AI Pipeline service

Tài liệu này định nghĩa những gì **mỗi regional KFP apiserver** (formerly
"core service") phải đáp ứng trong kiến trúc mới, khi FE giao tiếp trực tiếp
qua enforcer + gateway và CMP đã bỏ toàn bộ pipeline entity models.

> Tài liệu liên quan:
> - `docs/pipeline-gateway-requirements.md` — yêu cầu cho gateway (routing,
>   namespace resolution, enforcer, flavor/image catalog)
> - `docs/core-quota-requirements.md` — chi tiết contract quota (vẫn áp dụng
>   nguyên văn cho KFP)

---

## 1. Vai trò trong kiến trúc

```
FE ──► Enforcer ──► Core Gateway ──┬──► KFP vn-north-1 (service này)
                                    ├──► KFP vn-north-2
                                    └──► ...
```

Regional KFP là:

- **Source of truth** cho mọi pipeline entity trong region mình (Pipeline,
  PipelineVersion, Experiment, Run, Job)
- **Engine thực thi** — người phán quyết cuối cùng spec có hợp lệ hay không
- **Quota authority** trong region (enforcement atomic với resource creation)

Regional KFP **KHÔNG** lo nữa (so với thiết kế cũ):

| Trách nhiệm bỏ | Ai nhận |
|---|---|
| Auth/authz user | Enforcer + gateway (request đến đã có namespace resolved) |
| Region routing / xử lý region | Gateway strip region trước khi forward |
| Flavor/image catalog + resolve `flavor_id`/`image_id` | Gateway resolve sẵn vào spec trước khi forward |
| Mirror state về CMP (job→runs discovery) | Đã bỏ — CMP không còn Run model |

---

## 2. Namespace injection mechanism (cần chốt với gateway team)

Gateway sẽ resolve `Project-ID` header → namespace và inject vào proxied
request. **Hai phía phải chốt chung 1 cơ chế**:

| Lựa chọn | Cơ chế |
|---|---|
| (A) Namespaced path | Gateway rewrite path: `/apis/v2beta1/namespaces/{namespace}/runs/...` — KFP multi-user mode |
| (B) Identity header | Gateway set `Kubeflow-UserId` / header riêng, KFP derive namespace từ đó |

Yêu cầu bất kể cơ chế:

| # | Yêu cầu |
|---|---|
| 2.1 | Mọi project-scoped resource (pipeline, version, experiment, run, job) phải scope theo namespace của request — không bao giờ trả thấy resource cross-namespace |
| 2.2 | List endpoints chỉ trả resource của namespace đó |
| 2.3 | Request thao tác resource không thuộc namespace → 404 (không leak sự tồn tại) |
| 2.4 | Forward user identity (`X-CMP-User-GID` từ gateway) vào audit log / `Kubeflow-UserId` cho traceability |

---

## 3. Namespace provisioning trong cluster

Gateway gọi KFP trong bước provisioning fan-out (khi CMP activate service cho
project + region):

```
POST /apis/v2beta1/namespaces        (hoặc cơ chế K8s namespace có sẵn)
{ "namespace": "<project_slug>-pipeline" }
```

| # | Yêu cầu |
|---|---|
| 3.1 | Idempotent — gọi lại khi namespace đã tồn tại → thành công, không lỗi |
| 3.2 | Đảm bảo namespace đủ điều kiện chạy KFP: label `managed-by=pipeline-core` (network policy), service account KFP cần thiết |
| 3.3 | Trả lỗi rõ ràng nếu tạo thất bại — gateway giữ trạng thái `provisioning` và retry |

---

## 4. Spec upload & validation

Gateway đã resolve `flavor_id`/`image_id` thành `image_url` + resources cụ thể
trước khi forward (mục 9.1 của gateway doc). KFP nhận spec **đã resolve**.

| # | Yêu cầu |
|---|---|
| 4.1 | Nhận multipart YAML upload cho `POST /pipelines/` và `POST /pipeline-versions/` (giữ contract cũ) |
| 4.2 | **Validate cấu trúc spec**: KFP IR hợp lệ, component references đúng, syntax — theo đúng phiên bản KFP đang chạy ở region |
| 4.3 | **Validate `resource_envelope`** trong spec — contract chi tiết xem `core-quota-requirements.md`: `calculation` ∈ {EXACT, UPPER_BOUND}, `requests.cpu`/`memory` là Kubernetes quantities hợp lệ, `gpus[].gpu_model` được hỗ trợ, `nvidia.com/gpu > 0` đòi hỏi `gpus[]` không rỗng |
| 4.4 | Spec invalid → reject với lỗi chỉ rõ vị trí/field sai (không lưu spec lỗi) |
| 4.5 | `GET /pipeline-versions/{id}` trả lại `resource_envelope` đã parse — gateway/CMP đọc về để tính quota |

---

## 5. Entity CRUD & state machine

### Run states (tập cố định)

| State | Nhóm |
|---|---|
| `CREATING`, `SUBMITTED`, `QUEUED`, `PENDING`, `RUNNING` | Active (đang chiếm quota) |
| `SUCCEEDED`, `FAILED`, `CANCELED`, `SKIPPED` | Terminal (quota đã release) |

### Job states

| State | Ý nghĩa |
|---|---|
| `ENABLED` | Job active, tạo runs theo schedule |
| `DISABLED` | Job tắt |

| # | Yêu cầu |
|---|---|
| 5.1 | GET endpoints luôn phản ánh state hiện tại chính xác |
| 5.2 | Run terminal phải có `finished_at` timestamp |
| 5.3 | Run có `storage_state` (`AVAILABLE/ARCHIVED/CREATING/DELETING`) |
| 5.4 | Stable UUID trả ngay khi create — client dùng ID này cho mọi thao tác tiếp theo |

---

## 6. Relational validation (atomic với write)

Toàn bộ quan hệ giữa các entity được KFP validate — là owner duy nhất của dữ
liệu. Check phải **trong cùng transaction** với write (tránh TOCTOU).

| Tạo/xóa | Validate |
|---|---|
| Tạo Run | `pipeline_version_id` tồn tại + status READY + cùng project với namespace; `experiment_id` tồn tại + cùng project |
| Tạo Job | Như Run + schedule hợp lệ |
| Tạo PipelineVersion | `pipeline_id` tồn tại + cùng project |
| Xóa Pipeline | Block nếu còn version có Run/Job active |
| Xóa PipelineVersion | Block nếu còn Run/Job dùng version |
| Xóa Experiment | Block nếu còn Run/Job thuộc experiment |
| Xóa Job | Block nếu còn run (trừ khi `cascade=true`) |

Validation theo spec:

| # | Yêu cầu |
|---|---|
| 6.1 | `cron_schedule` phải là cron 5-field hợp lệ; `periodic_interval_second > 0` |
| 6.2 | `schedule_type` CRON ⟺ có `cron_schedule`; PERIODIC ⟺ có `periodic_interval_second` (XOR) |
| 6.3 | `max_concurrency` trong range 1–10 |
| 6.4 | Run/Job `parameters` phải khớp parameter definitions trong spec của version (tên param tồn tại, type/value hợp lệ) |

Lỗi trả về phải nêu rõ entity nào vi phạm (ID + lý do) để FE hiển thị được.

**Cross-region consistency**: không cần check riêng — mỗi request đến đúng 1
KFP, mọi entity liên quan cùng sống trong region này. FE tham chiếu sai region
→ 404 "not found" tự nhiên.

---

## 7. Deletion safety

| # | Yêu cầu |
|---|---|
| 7.1 | Block delete khi còn dependent (bảng ở mục 6) — lỗi nêu cụ thể resource đang block |
| 7.2 | Hỗ trợ `cascade=true` flag: xóa Pipeline → xóa versions; xóa Job → xóa runs của job |
| 7.3 | Xóa xong → GET trả 404 nhất quán (watcher/completion logic dựa vào đó) |

---

## 8. Quota enforcement

Contract nguyên vẹn từ `core-quota-requirements.md` — tóm tắt:

| Yêu cầu | Mô tả |
|---|---|
| `PUT /quota/limits` | Nhận limit theo project (namespace) từ CMP — qua gateway routing |
| Enforce khi tạo Run/Job | Atomic check-and-reserve; vượt → HTTP 422/429 với chi tiết `{quota_unit, limit, used, requested}` |
| `POST /quota/usage/bulk` | Trả `used` cho list project_ids — CMP sync hourly |
| `POST /quota/usage` (single) | Trả `used` cho 1 project |
| Tự release | Run/Job terminal → tự giải phóng resource khỏi `used` |

| # | Yêu cầu bổ sung |
|---|---|
| 8.1 | Enforcement scope theo **namespace** (được gateway inject) — không nhận project_id từ client payload cho mục đích quota |
| 8.2 | `used` tính theo resource đang active (không tính terminal), phản ánh envelope EXACT/UPPER_BOUND tương ứng |

---

## 9. Checklist tóm tắt

```
Namespace (mục 2, 3)
  ☐ Chốt cơ chế injection với gateway team (namespaced path vs UserId header)
  ☐ Scope mọi project-scoped resource theo namespace; cross-namespace → 404
  ☐ Namespace provisioning endpoint idempotent + đủ label/SA cho KFP

Spec (mục 4)
  ☐ Multipart YAML upload (contract cũ)
  ☐ Validate cấu trúc spec + resource_envelope; lỗi chỉ rõ field sai
  ☐ GET version trả resource_envelope đã parse

Entity & states (mục 5)
  ☐ Run/Job state machine chuẩn; terminal có finished_at
  ☐ Stable UUID ngay khi create

Relational validation (mục 6)
  ☐ FK checks atomic với write (version READY, experiment cùng project...)
  ☐ Cron/periodic XOR, max_concurrency, parameters khớp spec
  ☐ Lỗi nêu rõ entity vi phạm

Deletion (mục 7)
  ☐ Block khi còn dependent; cascade flag; 404 sau khi xóa xong

Quota (mục 8)
  ☐ PUT limits, atomic enforce, usage/bulk, auto-release
  ☐ Scope theo namespace, không tin project_id từ client
```
