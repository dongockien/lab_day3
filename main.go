package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// ─── Global state ────────────────────────────────────────────────────────────

var (
	db      *sql.DB
	rdb     *redis.Client
	sfGroup singleflight.Group

	// Bộ đếm atomic — xuất ra /stats để đo số query thật sự xuống DB
	dbQueryCount atomic.Int64
)

// ─── Model ───────────────────────────────────────────────────────────────────

type Disease struct {
	ID          int    `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	dsn := os.Getenv("DB_DSN")
	redisAddr := os.Getenv("REDIS_ADDR")

	// ── Kết nối PostgreSQL với connection pool hợp lý ────────────────────────
	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Giới hạn connection pool — tránh stampede mở vô số kết nối đến Postgres
	db.SetMaxOpenConns(25)               // tối đa 25 kết nối mở đồng thời
	db.SetMaxIdleConns(10)               // giữ sẵn 10 kết nối rảnh để tái dùng
	db.SetConnMaxLifetime(5 * time.Minute) // tái tạo kết nối già để tránh leak

	// Ping để xác nhận DB thật sự sẵn sàng (healthcheck khi khởi động)
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = db.PingContext(pingCtx); err != nil {
		log.Fatalf("Cannot reach database: %v", err)
	}
	log.Println("✅ Database connected")

	// ── Kết nối Redis ─────────────────────────────────────────────────────────
	rdb = redis.NewClient(&redis.Options{
		Addr:         redisAddr,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolSize:     20, // connection pool tới Redis
	})
	rCtx, rCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer rCancel()
	if err = rdb.Ping(rCtx).Err(); err != nil {
		log.Fatalf("Cannot reach Redis: %v", err)
	}
	log.Println("✅ Redis connected")

	// ── Routes ────────────────────────────────────────────────────────────────
	http.HandleFunc("/api/diseases/nocache", noCacheHandler)
	http.HandleFunc("/api/diseases/cacheaside", cacheAsideHandler)
	http.HandleFunc("/api/diseases/stampede", cacheStampedeHandler)
	http.HandleFunc("/api/diseases/singleflight", singleflightHandler)
	http.HandleFunc("/stats", statsHandler)   // đếm DB query, dùng để đo lab
	http.HandleFunc("/stats/reset", statsResetHandler) // reset bộ đếm trước mỗi kịch bản

	log.Println("🚀 Server is running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ─── Handlers ────────────────────────────────────────────────────────────────

// L1 — Không cache: mọi request đều query Postgres
func noCacheHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := codeParam(r)

	disease, err := queryDatabase(ctx, code)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, disease)
}

// L2 — Cache-aside: TTL 30 giây, hit rate cao, ít MISS
func cacheAsideHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := codeParam(r)
	cacheKey := "disease:aside:" + code

	// Cache HIT
	if val, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		w.Write(val)
		return
	}

	// Cache MISS → đọc DB
	log.Printf("[CACHE-ASIDE] MISS for key: %s", cacheKey)
	disease, err := queryDatabase(ctx, code)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(disease)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	// Ghi vào Redis, TTL 30 giây; log nếu ghi thất bại (Redis down)
	if setErr := rdb.Set(ctx, cacheKey, data, 30*time.Second).Err(); setErr != nil {
		log.Printf("[CACHE-ASIDE] Redis SET error for key %s: %v", cacheKey, setErr)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	w.Write(data)
}

// L3 — Stampede: TTL 5 giây để dễ thấy bão truy vấn khi key hết hạn
func cacheStampedeHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := codeParam(r)
	cacheKey := "disease:stampede:" + code

	// Cache HIT
	if val, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		w.Write(val)
		return
	}

	// Cache MISS — 500 VU đồng loạt chạm vào đây khi key hết hạn → stampede
	log.Printf("[STAMPEDE] ⚠️  Cache MISS! Hàng loạt request lao vào DB cho key: %s", cacheKey)
	disease, err := queryDatabase(ctx, code)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(disease)
	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	// TTL cực ngắn 5 giây để tái hiện stampede dễ dàng
	if setErr := rdb.Set(ctx, cacheKey, data, 5*time.Second).Err(); setErr != nil {
		log.Printf("[STAMPEDE] Redis SET error: %v", setErr)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	w.Write(data)
}

// L4 — Singleflight: gom toàn bộ request MISS vào 1 truy vấn DB duy nhất
func singleflightHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := codeParam(r)
	cacheKey := "disease:sf:" + code

	// Cache HIT — nhánh nhanh, không qua singleflight
	if val, err := rdb.Get(ctx, cacheKey).Bytes(); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "HIT")
		w.Write(val)
		return
	}

	// Cache MISS → đưa vào singleflight
	v, err, shared := sfGroup.Do(cacheKey, func() (interface{}, error) {
		// Double-check bên trong Do() — goroutine thứ 2 trở đi có thể thấy HIT
		if val, err := rdb.Get(context.Background(), cacheKey).Bytes(); err == nil {
			return val, nil
		}

		// Context riêng có timeout — KHÔNG dùng r.Context() của request đầu:
		// nếu request đó bị cancel (client ngắt), mọi goroutine chờ cùng thất bại.
		lctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		log.Printf("[SINGLEFLIGHT] Query DB cho key: %s (shared call)", cacheKey)
		disease, err := queryDatabase(lctx, code)
		if err != nil {
			return nil, err
		}

		data, err := json.Marshal(disease)
		if err != nil {
			return nil, err
		}

		if setErr := rdb.Set(lctx, cacheKey, data, 5*time.Second).Err(); setErr != nil {
			log.Printf("[SINGLEFLIGHT] Redis SET error: %v", setErr)
		}
		return data, nil
	})

	if err != nil {
		httpError(w, err, http.StatusInternalServerError)
		return
	}

	if shared {
		log.Printf("[SINGLEFLIGHT] Request được chia sẻ kết quả cho key: %s", cacheKey)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS-SF")
	w.Write(v.([]byte))
}

// /stats — trả về số lần queryDatabase đã được gọi (= số query thật xuống DB)
func statsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int64{
		"db_queries": dbQueryCount.Load(),
	})
}

// /stats/reset — đặt lại bộ đếm trước mỗi kịch bản
func statsResetHandler(w http.ResponseWriter, r *http.Request) {
	dbQueryCount.Store(0)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "reset"})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// queryDatabase đọc một bệnh theo code từ Postgres.
// Có 50ms sleep để giả lập I/O thực tế và làm rõ tác động của cache.
func queryDatabase(ctx context.Context, code string) (*Disease, error) {
	dbQueryCount.Add(1) // đếm query để xuất ra /stats

	// Giả lập độ trễ I/O của Database thực tế
	time.Sleep(50 * time.Millisecond)

	var d Disease
	err := db.QueryRowContext(
		ctx,
		"SELECT id, code, name, description, created_at FROM diseases WHERE code = $1",
		code,
	).Scan(&d.ID, &d.Code, &d.Name, &d.Description, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// codeParam đọc query param ?code=; mặc định D001 nếu không truyền.
func codeParam(r *http.Request) string {
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 50 {
		return "D001"
	}
	return code
}

// writeJSON ghi struct ra response JSON.
func writeJSON(w http.ResponseWriter, v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "marshal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// httpError ghi lỗi và log.
func httpError(w http.ResponseWriter, err error, code int) {
	log.Printf("[ERROR] %v", err)
	http.Error(w, err.Error(), code)
}
