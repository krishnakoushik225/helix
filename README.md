# Helix

Helix is a production-grade AI inference gateway written in Go that sits in front of Anthropic, OpenAI, and Ollama, providing automatic provider routing, semantic response caching, per-tenant JWT auth, Redis rate limiting, Prometheus observability, and a circuit-breaker fallback chain — all behind a single API.

## Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│  Client (curl / dashboard / SDK)                                     │
└───────────────────────────────┬──────────────────────────────────────┘
                                │  HTTPS + Bearer JWT
                                ▼
┌──────────────────────────────────────────────────────────────────────┐
│  Helix Gateway  (Go / chi)                                           │
│                                                                      │
│  ┌──────────────┐  ┌───────────────┐  ┌──────────────────────────┐  │
│  │  JWT Auth    │→ │  Rate Limiter │→ │  /v1/chat                │  │
│  │  (HS256)     │  │  (Redis Lua)  │  │  /v1/chat/stream (SSE)   │  │
│  └──────────────┘  └───────────────┘  └────────────┬─────────────┘  │
│                                                     │                │
│          ┌──────────────────────────────────────────┼──────────┐    │
│          ▼                                          ▼          │    │
│  ┌───────────────┐                        ┌─────────────────┐  │    │
│  │ Semantic Cache│ pgvector cosine sim    │ Inference Router│  │    │
│  │ (pgvector)    │ threshold 0.92 default │ score = latency │  │    │
│  └───────┬───────┘                        │ × cost × avail  │  │    │
│          │ cache miss                     └────────┬────────┘  │    │
│          │                                         │           │    │
│          │              ┌──────────────────────────┼────────┐  │    │
│          │              ▼                          ▼        ▼  │    │
│          │   ┌──────────────┐  ┌────────────┐  ┌────────┐     │    │
│          │   │  Anthropic   │  │  OpenAI    │  │ Ollama │     │    │
│          │   │  claude-*    │  │  gpt-4o-*  │  │ llama3 │     │    │
│          │   └──────────────┘  └────────────┘  └────────┘     │    │
│          │              Circuit Breaker (closed/half_open/open) │    │
│          └──────────────────────────────────────────────────────┘    │
│                                                                      │
│  ┌─────────────────────────┐  ┌───────────────────────────────────┐ │
│  │  Supabase (Postgres)    │  │  Prometheus /metrics              │ │
│  │  requests · prompt_cache│  │  Grafana dashboards               │ │
│  │  tenants · health       │  └───────────────────────────────────┘ │
│  └─────────────────────────┘                                        │
└──────────────────────────────────────────────────────────────────────┘
```

## Features

- **Multi-provider routing** — Anthropic, OpenAI, and Ollama behind a single endpoint; the router scores each provider on p95 latency, cost-per-token, and circuit health, then picks the best one automatically
- **Semantic cache** — pgvector cosine-similarity lookup (threshold 0.92 by default) deduplicates semantically equivalent prompts with OpenAI `text-embedding-3-small`; cache hits return in <10 ms with `X-Cache-Hit: true`
- **Circuit breaker** — 5 failures in 60 s opens the circuit; 30 s cooldown then probes with one half-open request; state is persisted in Supabase across restarts
- **SSE streaming** — `POST /v1/chat/stream` proxies token-by-token deltas from any provider; client disconnect cancels the upstream goroutine with no leak
- **JWT auth** — HS256 bearer token with `tenant_id` claim; middleware runs before rate limiting so all downstream code gets tenant context
- **Redis token bucket** — atomic Lua script enforces per-tenant RPM cap with sub-millisecond overhead; fails open on Redis outage
- **Prometheus metrics** — `helix_requests_total`, `helix_request_duration_seconds`, `helix_active_streams`, `helix_tokens_total`, `helix_cache_hits_total`, `helix_cost_usd_total`; Grafana dashboard included
- **Request logging** — every inference call is logged to Supabase with provider, model, latency, token counts, cost, and cache-hit flag
- **React dashboard** — live-polling UI (Vite + Tailwind + Recharts) showing request feed, latency chart, cache hit rate, cost tracker, and provider circuit state

## Tech stack

| Layer | Technology |
|---|---|
| Gateway | Go 1.25, [chi](https://github.com/go-chi/chi) router |
| Providers | Anthropic Messages API, OpenAI Chat Completions, Ollama |
| Auth | `golang-jwt/jwt` v5, HS256 |
| Rate limiting | Upstash Redis, `go-redis/v9`, Lua token bucket |
| Semantic cache | Supabase `pgvector`, OpenAI `text-embedding-3-small` |
| Database | Supabase (PostgreSQL + pgvector), `jackc/pgx` v5 |
| Observability | Prometheus, Grafana, `prometheus/client_golang` |
| Logging | `rs/zerolog` structured JSON |
| Config | `spf13/viper` (.env + env var override) |
| Dashboard | React 18, TypeScript, Vite, Tailwind CSS, Recharts |
| Deployment | Fly.io (gateway), Vercel (dashboard) |
| CI/CD | GitHub Actions, golangci-lint v2 |
| Load testing | k6 |

## API reference

All protected endpoints require `Authorization: Bearer <jwt>`.

### Public

| Method | Path | Description |
|---|---|---|
| GET | `/health` | Liveness check — always 200 `{"status":"ok"}` |
| GET | `/ready` | Readiness check — always 200 `{"status":"ready"}` |
| GET | `/metrics` | Prometheus metrics scrape endpoint |

### Protected (JWT required)

#### `POST /v1/chat`

Blocking inference. Returns when the full response is available.

**Request**

```json
{
  "provider": "anthropic",
  "model": "claude-haiku-4-5-20251001",
  "messages": [
    { "role": "system", "content": "You are a helpful assistant." },
    { "role": "user", "content": "Explain goroutines in one sentence." }
  ],
  "max_tokens": 512,
  "temperature": 0.7
}
```

- `provider` — `"anthropic"` | `"openai"` | `"ollama"` | omit for auto-routing
- `model` — provider model name; omit or `"auto"` to use the provider's default
- `messages` — non-empty array of `{role, content}` objects
- `stream` — must be `false` or absent; use `/v1/chat/stream` for streaming

**Response**

```json
{
  "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
  "provider": "anthropic",
  "model": "claude-haiku-4-5-20251001",
  "content": "Goroutines are lightweight, cooperatively scheduled threads managed by the Go runtime...",
  "input_tokens": 24,
  "output_tokens": 41,
  "finish_reason": "end_turn"
}
```

Cache hits add `X-Cache-Hit: true` to response headers.

#### `POST /v1/chat/stream`

SSE streaming inference. Each chunk is a `data: <delta>\n\n` line; stream ends with `data: [DONE]\n\n`.

Request body is the same shape as `/v1/chat`. Test with:

```bash
curl -N -X POST https://helix-serene-dew-2515.fly.dev/v1/chat/stream \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"Count to 5"}]}'
```

#### `GET /v1/stats`

Aggregate statistics from the requests table.

```json
{
  "total_requests": 1482,
  "cache_hit_rate": 34.7,
  "avg_latency_ms": 812,
  "cost_today_usd": 0.0041,
  "provider_breakdown": {
    "anthropic": { "requests": 800, "cost_usd": 0.0038, "avg_latency_ms": 920 },
    "openai":    { "requests": 500, "cost_usd": 0.0003, "avg_latency_ms": 680 },
    "ollama":    { "requests": 182, "cost_usd": 0.0,    "avg_latency_ms": 2100 }
  }
}
```

## Performance results

Results from a k6 run against the Fly.io deployment with 3 providers active and semantic cache warmed:

```
scenarios: (100.00%) 1 scenario, 100 max VUs, 4m00s max duration
  default: Up to 100 looping VUs for 3m30s

✓ chat: status 200
✓ chat: has content
✓ stream: status 200
✓ stream: has data lines
✓ stream: ends with DONE

checks.........................: 99.X%
data_received..................: X MB
data_sent......................: X MB
http_req_duration..............: avg=Xms  p(95)=Xms  p(99)=Xms
http_req_failed................: 0.XX%
stream_latency.................: avg=Xms  p(95)=Xms
cache_hit_rate.................: X.XX%    (X out of X requests)
```

> **Note:** Replace `X` placeholders with real output from `k6 run tests/load/k6_benchmark.js` against your deployment. See [Load testing](#load-testing) below.

## Semantic cache design

Helix caches LLM responses by semantic similarity rather than exact prompt matching, so "What is machine learning?" and "Can you explain machine learning?" return the same cached answer.

**How it works:**

1. On each request, the prompt (serialised message array) is sent to OpenAI `text-embedding-3-small`, yielding a 1536-dimensional float32 vector.
2. A pgvector `ivfflat` index (`vector_cosine_ops`) finds the nearest stored embedding using `1 - (embedding <=> $query)`.
3. If the cosine similarity exceeds the threshold (default 0.92, set via `CACHE_SIMILARITY_THRESHOLD`), the cached response is returned immediately with `X-Cache-Hit: true` — no LLM call is made.
4. On a miss, the provider response is written to `prompt_cache` asynchronously (goroutine) so cache population never adds latency to the caller.

**Threshold guidance:**

| Threshold | Behaviour |
|---|---|
| 0.99 | Near-exact match only |
| 0.92 (default) | Semantically equivalent questions match |
| 0.80 | Broader topic match; may return off-topic cached answers |

**Cost:** each unique prompt costs ~$0.00002 in OpenAI embedding credits. Repeated prompts and semantic near-duplicates pay nothing after the first call.

## Fallback chain and circuit breaker

The inference router tries providers in score order. If a provider fails, the circuit breaker records the failure and the next provider is tried immediately.

**Scoring formula:**

```
score = (1 / p95_latency_ms) × 0.4
      + (1 / cost_per_input_token) × 0.4
      + availability × 0.2
```

Free providers (Ollama, `cost = 0`) use `1e-9` as the cost floor so they rank above paid providers on the cost axis while still losing to a faster paid provider.

**Circuit breaker states:**

```
        5 failures / 60 s
CLOSED ──────────────────► OPEN
  ▲                          │
  │   success                │ 30 s cooldown
  │                          ▼
  └────────────────── HALF_OPEN
         probe allowed (1 req)
```

- **Closed** — normal; all requests routed here
- **Open** — skipped entirely by router; availability = 0.0
- **Half-open** — one probe request allowed; success closes the circuit, failure reopens it

State is persisted to the `provider_health` Supabase table so circuit memory survives gateway restarts.

## Local development

### Prerequisites

- Go 1.25+
- [Ollama](https://ollama.com) (optional, for free local inference)
- Supabase project with the schema below (optional, disables DB features if absent)
- Upstash Redis instance (optional, disables rate limiting if absent)

### 1. Clone and configure

```bash
git clone https://github.com/krishnakoushik225/helix
cd helix
cp .env.example .env
```

Edit `.env`:

```env
PORT=8080
ENV=development
JWT_SECRET=dev-secret-change-me

# Provider keys (set the ones you have; omit the rest)
ANTHROPIC_API_KEY=sk-ant-...
OPENAI_API_KEY=sk-...
OLLAMA_BASE_URL=http://localhost:11434

# Optional — request logging and semantic cache
DATABASE_URL=postgresql://postgres:...@db.<project>.supabase.co:5432/postgres

# Optional — rate limiting
REDIS_URL=rediss://default:...@...upstash.io:6379

# Optional — semantic cache (requires DATABASE_URL + OPENAI_API_KEY)
CACHE_ENABLED=true
CACHE_SIMILARITY_THRESHOLD=0.92

RATE_LIMIT_RPM=60
```

### 2. Supabase schema (if using DATABASE_URL)

Run this in the Supabase SQL editor:

```sql
create extension if not exists vector;

create table tenants (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  api_key text unique not null,
  daily_budget_usd numeric(10,4) default 10.0,
  created_at timestamptz default now()
);

create table prompt_cache (
  id uuid primary key default gen_random_uuid(),
  prompt_hash text not null,
  prompt text not null,
  response text not null,
  provider text not null,
  embedding vector(1536),
  hit_count int default 0,
  created_at timestamptz default now()
);
create index on prompt_cache using ivfflat (embedding vector_cosine_ops) with (lists = 100);

create table requests (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid references tenants(id),
  provider text not null,
  model text not null,
  prompt_tokens int,
  completion_tokens int,
  cost_usd numeric(10,6),
  latency_ms int,
  cache_hit boolean default false,
  created_at timestamptz default now()
);

create table provider_health (
  provider text primary key,
  state text default 'closed',
  failure_count int default 0,
  last_failure_at timestamptz,
  opened_at timestamptz,
  updated_at timestamptz default now()
);

insert into tenants (name, api_key) values ('dev', 'test-key-123');
```

### 3. Start Ollama (optional)

```bash
ollama serve          # in a separate terminal
ollama pull llama3
```

### 4. Run the gateway

```bash
make run
# or: go run ./cmd/helix
```

Verify:

```bash
curl localhost:8080/health
# {"status":"ok"}
```

### 5. Generate a JWT

```bash
make gen-token
# prints: eyJhbGci...
export TOKEN=<paste token>
```

### 6. Send a test request

```bash
curl -X POST localhost:8080/v1/chat \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"say hello"}]}'
```

### 7. Start the dashboard

```bash
cd dashboard
cp .env.local.example .env.local   # set VITE_API_URL=http://localhost:8080
npm install
npm run dev
# open http://localhost:5173
```

### 8. Start Prometheus + Grafana (optional)

```bash
docker compose up -d
# Prometheus: http://localhost:9090
# Grafana:    http://localhost:3001  (add Prometheus source: http://prometheus:9090)
```

## Deployment

### Fly.io (gateway)

```bash
# One-time setup
flyctl auth login
flyctl launch   # answer no to immediate deploy

# Set secrets
flyctl secrets set \
  DATABASE_URL="..." \
  REDIS_URL="..." \
  ANTHROPIC_API_KEY="..." \
  OPENAI_API_KEY="..." \
  JWT_SECRET="$(openssl rand -hex 32)"

# Deploy
git push origin main   # GitHub Actions deploys automatically on push to main
# or manually:
flyctl deploy --remote-only
```

Health check: `curl https://helix-serene-dew-2515.fly.dev/health`

### Vercel (dashboard)

1. Go to [vercel.com](https://vercel.com) → **Add New Project** → import this repo
2. Set **Root Directory** to `dashboard`
3. Add environment variable: `VITE_API_URL=https://helix-serene-dew-2515.fly.dev`
4. Deploy — Vercel detects Vite automatically

The `vercel.json` in the repo root already sets `rootDirectory: "dashboard"` and `framework: "vite"`.

## Load testing

Requires [k6](https://k6.io/docs/get-started/installation/) ≥ 0.46.

```bash
# Quick smoke test (local)
k6 run \
  -e HELIX_URL=http://localhost:8080 \
  -e HELIX_TOKEN=$TOKEN \
  tests/load/k6_benchmark.js

# Full benchmark against production
k6 run \
  -e HELIX_URL=https://helix-serene-dew-2515.fly.dev \
  -e HELIX_TOKEN=$PROD_TOKEN \
  tests/load/k6_benchmark.js
```

**Test profile:**

| Stage | Duration | VUs |
|---|---|---|
| Ramp-up | 30 s | 0 → 10 |
| Sustain | 2 min | 50 |
| Spike | 30 s | 100 |
| Ramp-down | 30 s | 0 |

**Thresholds:**

- `http_req_duration p(95) < 3000 ms`
- `http_req_failed rate < 1 %`
- `stream_latency p(95) < 3000 ms`

40 % of requests reuse a small fixed prompt pool to exercise the semantic cache. Both `/v1/chat` and `/v1/chat/stream` are tested in each VU iteration.
