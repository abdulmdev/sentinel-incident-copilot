// Load test: docker run --rm -v "$PWD/deploy/k6:/scripts" --add-host=host.docker.internal:host-gateway \
//   grafana/k6 run /scripts/load.js   (or `scripts/load-test.sh`)
//
// incidents  sustained incident creation across many tenants: gateway -> agent (LangGraph, Postgres
//            checkpoints) -> MCP servers -> chaos-lab, full path, rule-based investigator
// search     hybrid retrieval (embedding + pgvector + full-text + RRF) throughput
// ratelimit  one tenant bursts 200 requests at once: the distributed bucket must admit exactly
//            its capacity (120) across concurrent requests, no more
import http from 'k6/http';
import { check } from 'k6';
import crypto from 'k6/crypto';
import encoding from 'k6/encoding';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://host.docker.internal:8080';
const SECRET = __ENV.SENTINEL_JWT_SECRET || 'dev-only-secret-change-me-0123456789abcdef';

const admitted = new Counter('ratelimit_admitted');
const rejected = new Counter('ratelimit_rejected');

function jwt(sub, tenant) {
  const b64 = (o) => encoding.b64encode(JSON.stringify(o), 'rawurl');
  const now = Math.floor(Date.now() / 1000);
  const unsigned = `${b64({ alg: 'HS256', typ: 'JWT' })}.${b64({ sub, tenant_id: tenant, iat: now, exp: now + 3600 })}`;
  return `${unsigned}.${crypto.hmac('sha256', SECRET, unsigned, 'base64rawurl')}`;
}

export const options = {
  scenarios: {
    incidents: { executor: 'constant-arrival-rate', exec: 'incident', rate: 5, timeUnit: '1s',
                 duration: '60s', preAllocatedVUs: 20, maxVUs: 60 },
    search: { executor: 'constant-arrival-rate', exec: 'search', rate: 20, timeUnit: '1s',
              duration: '60s', preAllocatedVUs: 20, maxVUs: 60 },
    ratelimit: { executor: 'per-vu-iterations', exec: 'burst', vus: 50, iterations: 4,
                 startTime: '65s', maxDuration: '30s' },
  },
  thresholds: {
    'http_req_duration{scenario:incidents}': ['p(95)<2000'],
    'http_req_duration{scenario:search}': ['p(95)<300'],
    'http_req_failed{scenario:incidents}': ['rate<0.01'],
    'http_req_failed{scenario:search}': ['rate<0.01'],
    'ratelimit_admitted': ['count<=125'],   // capacity 120 (+ refill during the burst)
  },
};

const SERVICES = ['checkout', 'inventory'];

export function incident() {
  const tenant = `load-${__VU % 20}`;   // 20 tenants: realistic spread, stays inside per-tenant budgets
  const res = http.post(`${BASE}/api/v1/incidents`,
    JSON.stringify({ service: SERVICES[__ITER % 2], summary: 'load test alert', severity: 'SEV3' }),
    { headers: { Authorization: `Bearer ${jwt(`k6-${__VU}`, tenant)}`, 'Content-Type': 'application/json' },
      tags: { name: 'POST /api/v1/incidents' } });
  check(res, { 'incident awaiting approval': (r) => r.status === 200 && r.json('status') === 'AWAITING_APPROVAL' });
}

const QUERIES = ['timeout acquiring DB connection', 'how do I roll back checkout', 'pods keep getting OOMKilled',
                 'upstream returned 503 from pricing-api', 'who do I page if primary does not answer'];

export function search() {
  const tenant = `acme-load-${__VU % 30}`;
  const q = encodeURIComponent(QUERIES[__ITER % QUERIES.length]);
  const res = http.get(`${BASE}/api/v1/knowledge/search?q=${q}&k=5`,
    { headers: { Authorization: `Bearer ${jwt(`k6-${__VU}`, tenant)}` }, tags: { name: 'GET /api/v1/knowledge/search' } });
  check(res, { 'search ok': (r) => r.status === 200 });
}

export function burst() {
  const res = http.get(`${BASE}/api/v1/knowledge/search?q=rollback&k=1`,
    { headers: { Authorization: `Bearer ${jwt('burster', 'burst-tenant')}` }, tags: { name: 'burst' },
      responseCallback: http.expectedStatuses(200, 429) });
  if (res.status === 200) admitted.add(1);
  if (res.status === 429) rejected.add(1);
  check(res, { '200 or 429 with Retry-After': (r) => r.status === 200 || (r.status === 429 && r.headers['Retry-After'] === '1') });
}
