# Lab Day 3 — Cache & Redis

> **Chủ đề:** Cache nhiều tầng, Redis data types & persistence, Cache Stampede, Singleflight  
> **Người thực hiện:** Kiên — NextX Engineering Team  
> **Ngày:** 28/10/2026

---

## Mục lục

- [Tổng quan](#tổng-quan)
- [Kiến trúc](#kiến-trúc)
- [Cấu trúc dự án](#cấu-trúc-dự-án)
- [Yêu cầu](#yêu-cầu)
- [Chạy Lab](#chạy-lab)
- [Bốn kịch bản đo](#bốn-kịch-bản-đo)
- [Đọc kết quả](#đọc-kết-quả)
- [Bài học rút ra](#bài-học-rút-ra)

---

## Tổng quan

Lab này đo lường tác động của cache Redis lên một API đọc danh mục bệnh từ PostgreSQL, qua 4 kịch bản tăng dần độ khó:

| Kịch bản | Mục tiêu |
|---|---|
| **L1 — No Cache** | Baseline: mọi request đều query Postgres |
| **L2 — Cache-aside** | Redis giảm tải DB thế nào (TTL 30s, 50 VU) |
| **L3 — Stampede** | Key nóng hết hạn (TTL 5s, 500 VU) → DB bị bão truy vấn |
| **L4 — Singleflight** | Như L3 nhưng dùng `singleflight.Group` để gom request |

Bản lý thuyết đầy đủ: [`Buoi_3_NextX.md`](./Buoi_3_NextX.md)

---

## Kiến trúc

```
k6 (Virtual Users)
      │
      ▼
┌─────────────────────┐
│   Go API (:8080)    │   4 endpoints × 4 kịch bản
│                     │   /stats  — đếm DB query
└──────┬──────────────┘
       │
       ├──► Redis :6379  (cache-aside, TTL 5–30s)
       │
       └──► PostgreSQL :5432  (bảng diseases, 5 bản ghi, +50ms giả lập I/O)
```

---

## Cấu trúc dự án

```
lab_day3/
├── main.go              # Go API — 4 handlers + /stats
├── load.js              # k6 script — cấu hình qua ENV
├── docker-compose.yml   # Postgres + Redis + App + k6
├── Dockerfile           # Multi-stage build (golang:1.24-alpine)
├── init.sql             # Seed 5 bản ghi diseases
├── go.mod / go.sum
└── Buoi_3_NextX.md      # Báo cáo lý thuyết + kết quả lab
```

---

## Yêu cầu

- [Docker Desktop](https://www.docker.com/products/docker-desktop/) ≥ 4.x
- (Tùy chọn) [k6](https://k6.io/docs/getting-started/installation/) nếu muốn chạy ngoài Docker

---

## Chạy Lab

### 1. Khởi động hệ thống

```bash
# Build và khởi động App + Postgres + Redis
docker compose up -d --build

# Kiểm tra tất cả healthy
docker compose ps
```

### 2. Kiểm tra API hoạt động

```bash
# Test nhanh
curl http://localhost:8080/api/diseases/nocache?code=D001
curl http://localhost:8080/stats
```

### 3. Chạy 4 kịch bản k6

> **Quan trọng:** Reset bộ đếm trước mỗi kịch bản để số liệu chính xác.

```bash
# L1 — Không cache (50 VU, 60 giây)
curl -X POST http://localhost:8080/stats/reset
docker compose run --rm -e ENDPOINT=nocache -e VUS=50 -e DURATION=60s k6 run /load.js
curl http://localhost:8080/stats

# L2 — Redis cache-aside (50 VU, 60 giây)
curl -X POST http://localhost:8080/stats/reset
docker compose run --rm -e ENDPOINT=cacheaside -e VUS=50 -e DURATION=60s k6 run /load.js
curl http://localhost:8080/stats

# L3 — Stampede (500 VU, 60 giây)
curl -X POST http://localhost:8080/stats/reset
docker compose run --rm -e ENDPOINT=stampede -e VUS=500 -e DURATION=60s k6 run /load.js
curl http://localhost:8080/stats

# L4 — Singleflight (500 VU, 60 giây)
curl -X POST http://localhost:8080/stats/reset
docker compose run --rm -e ENDPOINT=singleflight -e VUS=500 -e DURATION=60s k6 run /load.js
curl http://localhost:8080/stats
```

### 4. Dừng hệ thống

```bash
docker compose down -v   # -v xóa cả volume Postgres để reset sạch
```

---

## Bốn kịch bản đo

### L1 — Không cache (`/api/diseases/nocache`)

Mỗi request query Postgres trực tiếp. Đây là baseline để so sánh.

```
request → Go API → PostgreSQL (50ms I/O) → response
```

### L2 — Cache-aside (`/api/diseases/cacheaside`)

Pattern cache-aside chuẩn: kiểm tra Redis trước, MISS thì đọc DB rồi ghi Redis (TTL 30s).

```
request → Go API → Redis HIT? → response
                 → MISS → PostgreSQL → Redis SET(TTL=30s) → response
```

**Header `X-Cache`:** `HIT` hoặc `MISS` để dễ debug.

### L3 — Stampede (`/api/diseases/stampede`)

TTL 5 giây + 500 VU. Mỗi lần key hết hạn, toàn bộ VU đang chờ đồng loạt thấy MISS và cùng đập vào DB. Đây là **cache stampede** (thundering herd).

### L4 — Singleflight (`/api/diseases/singleflight`)

Dùng [`golang.org/x/sync/singleflight`](https://pkg.go.dev/golang.org/x/sync/singleflight). Khi nhiều goroutine cùng MISS cùng key, chỉ **một** goroutine query DB, các goroutine khác đứng chờ và nhận cùng kết quả.

**Chi tiết quan trọng trong implementation:**

```go
// Dùng context RIÊNG có timeout — KHÔNG dùng r.Context() của request đầu:
// nếu request đó bị cancel (client ngắt), mọi goroutine chờ cùng thất bại.
lctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
defer cancel()
```

---

## Đọc kết quả

### `/stats` — số DB query thực tế

```bash
curl http://localhost:8080/stats
# {"db_queries": 42}
```

So sánh `db_queries` giữa các kịch bản → thấy rõ cache và singleflight giảm tải DB bao nhiêu.

### Header `X-Cache`

| Giá trị | Ý nghĩa |
|---|---|
| `HIT` | Đọc từ Redis, không xuống DB |
| `MISS` | Cache miss, đọc DB |
| `MISS-SF` | Cache miss nhưng được singleflight gom |

---

## Bài học rút ra

1. **Hit rate cao là cách giảm tải DB rẻ nhất** — L2 gần như không có query DB sau lần đầu.
2. **TTL ngắn trên key nóng không tự nhiên an toàn** — L3 tạo ra điểm nổ định kỳ mỗi 5 giây.
3. **Singleflight biến N query thành 1 query trong một process** — nhưng chỉ trong một process; scale ra N instance vẫn có N query.
4. **Cache sai tệ hơn không có cache** — sửa DB mà không đổi tem Redis thì service đọc cấu hình cũ, không có lỗi, không có cảnh báo.
5. **Context phải truyền đúng** — dùng context riêng có timeout bên trong singleflight.Do, không dùng context của request đầu tiên.

---

## Tài liệu tham khảo

- [Redis Documentation — Data Types](https://redis.io/docs/data-types/)
- [Redis Documentation — Persistence](https://redis.io/docs/management/persistence/)
- ["Scaling Memcache at Facebook" — NSDI 2013](https://www.usenix.org/conference/nsdi13/technical-sessions/presentation/nishtala)
- [golang.org/x/sync/singleflight](https://pkg.go.dev/golang.org/x/sync/singleflight)
- [go-redis/v9](https://github.com/redis/go-redis)
