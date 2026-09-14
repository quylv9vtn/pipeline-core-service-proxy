# AI Pipeline – Core Quota Requirements

> Đã chuyển từ `cmp-backend/backend/ai_pipeline/docs/` sang repo này —
> contract quota giờ thuộc về regional KFP apiserver (region-scope, không còn
> knowledge về CMP models).

Tài liệu này mô tả những yêu cầu regional KFP cần đáp ứng để CMP có thể quản
lý quota cho dịch vụ AI Pipeline theo kiến trúc **imprecise quota + sync từ
KFP**.

---

## Bối cảnh

### Kiến trúc triển khai

Mỗi region có một instance regional KFP apiserver riêng biệt. CMP backend định
tuyến request đến đúng region thông qua core gateway (route theo region name).
Không có instance trung tâm.

Hệ quả:
- Mỗi KFP instance chỉ quản lý dữ liệu của region mình.
- `region_id` **không cần** truyền trong request đến KFP — gateway đã route
  đúng region trước khi forward.
- Scope dữ liệu của mỗi API call tại KFP là `(namespace, region của KFP đó)`,
  với namespace do gateway inject từ `Project-ID` header.

### Vai trò

CMP đóng vai trò:
- Kích hoạt dịch vụ và đồng bộ **limit** xuống KFP của từng region (qua gateway).
- Định kỳ đọc giá trị **used** từ KFP của từng region (qua gateway) để hiển
  thị cho user và tính tiền PAYG.

Regional KFP đóng vai trò:
- **Authority** duy nhất về enforcement trong region của mình: tự siết tài
  nguyên khi user tạo Run/Job vượt limit.
- Nguồn dữ liệu `used` chính xác nhất cho region đó.

CMP **không** tự tính `used` từ trạng thái nội bộ.

---

## Các đơn vị quota cần theo dõi

| Quota unit               | Đơn vị | Ghi chú                              |
|--------------------------|--------|--------------------------------------|
| `ai_pipeline_cpu`        | core   | Số CPU core (làm tròn lên)           |
| `ai_pipeline_memory`     | GiB    | RAM (làm tròn lên theo GiB)          |
| `ai_pipeline_gpu_rtx2080` | GPU   | Số card RTX 2080                     |
| `ai_pipeline_gpu_h200`   | GPU    | Số card H200                         |

Scope: mỗi cặp `(project_id, region)` — trong đó `region` là region của KFP
instance đang nhận request.

---

## Yêu cầu 1 – Nhận và áp dụng limit từ CMP

### Mô tả

Khi CMP kích hoạt dịch vụ cho một project tại một region, hoặc khi admin
thay đổi limit, CMP sẽ gửi giá trị limit xuống KFP của region đó (qua
gateway). KFP phải lưu lại và dùng giá trị này để enforce ở bước tạo tài
nguyên.

### API

```
PUT /apis/v2beta1/quota/limits
```

**Request body:**

```json
{
  "project_id": "uuid",
  "limits": {
    "ai_pipeline_cpu": 64,
    "ai_pipeline_memory": 256,
    "ai_pipeline_gpu_rtx2080": 0,
    "ai_pipeline_gpu_h200": 4
  }
}
```

**Lưu ý:**
- CMP sẽ luôn gửi đủ tất cả các field trong `limits`.
- Nếu project chưa có limit, coi như tạo mới.
- Nếu limit mới nhỏ hơn `used` hiện tại, KFP vẫn phải chấp nhận (limit giảm
  chỉ có hiệu lực từ lần tạo tài nguyên tiếp theo, không xóa tài nguyên đang
  chạy).

---

## Yêu cầu 2 – Enforce limit khi tạo Run/Job

### Mô tả

Khi gateway forward request tạo Run hoặc Job, KFP phải kiểm tra xem tổng
resource yêu cầu có vượt limit của project đó không. Nếu vượt, KFP phải từ
chối request với HTTP 422 hoặc 429 (tuỳ convention KFP đang dùng) và trả về
thông báo rõ ràng cho user.

### Yêu cầu chi tiết

| # | Yêu cầu |
|---|---------|
| 2.1 | Enforcement phải **atomic** — kiểm tra và cộng `used` phải là một thao tác nguyên tử (SELECT FOR UPDATE hoặc tương đương) để tránh TOCTOU khi nhiều request đến đồng thời. |
| 2.2 | Scope enforcement là namespace (được gateway inject) trong region của KFP instance đó — không shared giữa các project. |
| 2.3 | Giá trị `used` tính theo resource **đang chạy hoặc đang khởi tạo**, không tính Run/Job đã kết thúc (terminal states). |
| 2.4 | Khi Run/Job kết thúc (terminal), KFP phải **tự động giải phóng** phần resource đó khỏi `used`. |
| 2.5 | Response khi vượt quota phải chứa thông tin quota unit nào bị vượt và giá trị cụ thể (`limit`, `used`, `requested`). |

**Ví dụ response khi vượt quota:**

```json
{
  "error": "quota_exceeded",
  "message": "Insufficient quota",
  "details": [
    {
      "quota_unit": "ai_pipeline_gpu_h200",
      "limit": 4,
      "used": 3,
      "requested": 2
    }
  ]
}
```

---

## Yêu cầu 3 – API trả về giá trị `used` hiện tại

### Mô tả

CMP định kỳ poll từng region (qua gateway) để đọc `used` của các project đang
hoạt động tại region đó.

### API — single project

```
GET /apis/v2beta1/quota/usage?project_id=uuid
```

**Response:**

```json
{
  "project_id": "uuid",
  "used": {
    "ai_pipeline_cpu": 12,
    "ai_pipeline_memory": 48,
    "ai_pipeline_gpu_rtx2080": 0,
    "ai_pipeline_gpu_h200": 2
  }
}
```

### API — bulk (nhiều project trong cùng một region)

```
POST /apis/v2beta1/quota/usage/bulk
```

```json
{
  "project_ids": ["uuid-1", "uuid-2", "uuid-3"]
}
```

```json
{
  "results": [
    {
      "project_id": "uuid-1",
      "used": { "ai_pipeline_cpu": 8, "ai_pipeline_memory": 32, "ai_pipeline_gpu_h200": 1, "ai_pipeline_gpu_rtx2080": 0 }
    },
    {
      "project_id": "uuid-2",
      "used": { "ai_pipeline_cpu": 0, "ai_pipeline_memory": 0, "ai_pipeline_gpu_h200": 0, "ai_pipeline_gpu_rtx2080": 0 }
    }
  ]
}
```

**Yêu cầu chi tiết:**

| # | Yêu cầu |
|---|---------|
| 3.1 | `used` phản ánh resource của các Run/Job đang **active** (không phải terminal). |
| 3.2 | Nếu project không có Run/Job nào đang active, các giá trị phải là `0`, không được bỏ qua field. |
| 3.3 | Nếu `project_id` không tồn tại hoặc chưa có limit tại KFP này, trả về tất cả giá trị `0` (không trả lỗi). |
| 3.4 | Bulk endpoint nên hỗ trợ ít nhất 100 `project_id` trong một request để tránh CMP phải gọi N lần khi sync nhiều project trong cùng region. |

---

## Tóm tắt

| # | Yêu cầu | Bắt buộc |
|---|---------|----------|
| 1 | `PUT /quota/limits` — nhận limit theo project | Có |
| 2 | Enforce limit khi tạo Run/Job, atomic, trả về lỗi có chi tiết | Có |
| 2.4 | Tự giải phóng resource khi Run/Job kết thúc | Có |
| 3 | `GET /quota/usage?project_id=` — đọc used cho một project | Có |
| 3 bulk | `POST /quota/usage/bulk` — đọc used cho nhiều project trong cùng region | Khuyến nghị |
