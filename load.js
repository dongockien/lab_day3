import http from 'k6/http';
import { check, sleep } from 'k6';

/**
 * Cấu hình kịch bản qua biến môi trường:
 *   ENDPOINT  = nocache | cacheaside | stampede | singleflight  (mặc định: stampede)
 *   VUS       = số virtual users                                (mặc định: 50)
 *   DURATION  = thời gian chạy                                  (mặc định: 60s)
 *   CODE      = mã bệnh                                         (mặc định: D001)
 *
 * Ví dụ chạy:
 *   docker compose run --rm k6 run -e ENDPOINT=nocache    -e VUS=50  -e DURATION=60s /load.js
 *   docker compose run --rm k6 run -e ENDPOINT=cacheaside -e VUS=50  -e DURATION=60s /load.js
 *   docker compose run --rm k6 run -e ENDPOINT=stampede   -e VUS=500 -e DURATION=60s /load.js
 *   docker compose run --rm k6 run -e ENDPOINT=singleflight -e VUS=500 -e DURATION=60s /load.js
 */

const BASE     = __ENV.BASE     || 'http://lab3_app:8080';
const ENDPOINT = __ENV.ENDPOINT || 'stampede';
const VUS      = parseInt(__ENV.VUS      || '50');
const DURATION = __ENV.DURATION || '60s';
const CODE     = __ENV.CODE     || 'D001';

export const options = {
  scenarios: {
    load: {
      executor: 'constant-vus',
      vus: VUS,
      duration: DURATION,
    },
  },
  thresholds: {
    // Ngưỡng để dễ so sánh giữa các kịch bản
    http_req_duration: ['p(95)<5000'], // p95 < 5 giây
    http_req_failed:   ['rate<0.05'],  // tỉ lệ lỗi < 5%
  },
};

export default function () {
  const url = `${BASE}/api/diseases/${ENDPOINT}?code=${CODE}`;
  const res = http.get(url, { timeout: '10s' });

  check(res, {
    'status is 200': (r) => r.status === 200,
    'has body':      (r) => r.body && r.body.length > 0,
  });

  // Không sleep để tạo áp lực tối đa và thấy rõ stampede
  // sleep(0.1);
}