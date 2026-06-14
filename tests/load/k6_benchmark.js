/**
 * Helix Gateway — k6 Load Test
 *
 * Usage:
 *   k6 run -e HELIX_URL=https://helix-serene-dew-2515.fly.dev \
 *          -e HELIX_TOKEN=eyJ... \
 *          tests/load/k6_benchmark.js
 *
 * Requires k6 ≥ 0.46. Install: https://k6.io/docs/get-started/installation/
 */

import http from 'k6/http'
import { check, group, sleep } from 'k6'
import { Rate, Trend } from 'k6/metrics'
import { SharedArray } from 'k6/data'

// ── Custom metrics ────────────────────────────────────────────────────────────

/** Tracks how often responses come from the semantic cache. */
const cacheHitRate = new Rate('cache_hit_rate')

/**
 * Tracks SSE stream end-to-end latency in milliseconds.
 * k6 buffers the full streaming response, so this is wall-clock time
 * from request send to the last [DONE] byte received.
 */
const streamLatency = new Trend('stream_latency', /* isTime= */ true)

// ── Prompt corpus ─────────────────────────────────────────────────────────────
//
// REPEAT_POOL (40 % of requests) uses the same short prompts so the semantic
// cache can serve hits once the embedding is stored after the first real call.
//
// UNIQUE_POOL (60 % of requests) exercises the fallback / provider routing
// path with diverse questions.

const REPEAT_POOL = new SharedArray('repeat_prompts', () => [
  'What is the capital of France?',
  'How does HTTPS work?',
  'Explain the difference between TCP and UDP.',
  'What is machine learning?',
])

const UNIQUE_POOL = new SharedArray('unique_prompts', () => [
  'Explain quantum entanglement in one sentence.',
  'Write a haiku about the ocean.',
  'What are the three laws of thermodynamics?',
  'How does a garbage collector work?',
  'Describe the CAP theorem in plain English.',
  'What is a microservice architecture?',
  'Explain eventual consistency in distributed systems.',
  'What is the difference between SQL and NoSQL databases?',
  'How does a CDN improve web performance?',
  'What is a JWT and how is it verified?',
  'Explain the observer design pattern.',
  'What is a Bloom filter used for?',
  'Describe the MapReduce programming model.',
  'What is a load balancer and why is it needed?',
  'Explain the difference between authorization and authentication.',
  'What is a binary search tree?',
  'Describe how the TLS handshake works step by step.',
  'What is the purpose of a message queue?',
  'Explain backpressure in streaming systems.',
  'What is the circuit breaker pattern and when should you use it?',
])

// ── Configuration ─────────────────────────────────────────────────────────────

export const options = {
  stages: [
    { duration: '30s', target: 10  },  // ramp to 10 VUs
    { duration: '2m',  target: 50  },  // sustain at 50 VUs
    { duration: '30s', target: 100 },  // spike to 100 VUs
    { duration: '30s', target: 0   },  // ramp down
  ],

  thresholds: {
    // Overall request latency (both endpoints combined)
    http_req_duration: ['p(95)<3000'],

    // Hard error-rate ceiling — anything above 1 % fails the test
    http_req_failed: ['rate<0.01'],

    // Streaming-specific latency measured via custom Trend
    stream_latency: ['p(95)<3000'],

    // Informational: at least some requests should be cache hits
    cache_hit_rate: ['rate>0'],
  },
}

// ── Module-level init (runs once per VU, before default function) ─────────────

const BASE_URL = (__ENV.HELIX_URL || 'http://localhost:8080').replace(/\/$/, '')
const TOKEN    = __ENV.HELIX_TOKEN || ''

// ── Setup (runs once before any VU starts) ────────────────────────────────────

export function setup() {
  if (!TOKEN) {
    // Fail immediately rather than generating a flood of 401s
    throw new Error(
      'HELIX_TOKEN is not set.\n' +
      'Pass it via:  k6 run -e HELIX_TOKEN=<jwt> ...',
    )
  }

  const res = http.get(`${BASE_URL}/health`)
  if (res.status !== 200) {
    throw new Error(
      `Helix health check failed: HTTP ${res.status}.\n` +
      `Verify HELIX_URL=${BASE_URL} is reachable.`,
    )
  }

  console.log(`✓ Helix reachable at ${BASE_URL}`)
  return { baseUrl: BASE_URL }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function pickPrompt() {
  if (Math.random() < 0.4) {
    return REPEAT_POOL[Math.floor(Math.random() * REPEAT_POOL.length)]
  }
  return UNIQUE_POOL[Math.floor(Math.random() * UNIQUE_POOL.length)]
}

function makeHeaders() {
  return {
    'Content-Type': 'application/json',
    Authorization:  `Bearer ${TOKEN}`,
  }
}

// ── Main VU loop ──────────────────────────────────────────────────────────────

export default function () {
  const prompt = pickPrompt()
  const body   = JSON.stringify({
    messages: [{ role: 'user', content: prompt }],
  })

  // Each VU randomly chooses between the blocking and streaming endpoint.
  if (Math.random() < 0.5) {
    testChat(body)
  } else {
    testStream(body)
  }

  // Think time: 0.5 – 2.5 s (simulates a real user reading the response)
  sleep(Math.random() * 2 + 0.5)
}

// ── /v1/chat (blocking) ───────────────────────────────────────────────────────

function testChat(body) {
  group('POST /v1/chat', () => {
    const res = http.post(`${BASE_URL}/v1/chat`, body, {
      headers: makeHeaders(),
    })

    check(res, {
      'chat: status 200': (r) => r.status === 200,
      'chat: has content': (r) => {
        try {
          const parsed = JSON.parse(r.body)
          return typeof parsed.content === 'string' && parsed.content.length > 0
        } catch (_) {
          return false
        }
      },
      'chat: has provider': (r) => {
        try {
          return JSON.parse(r.body).provider?.length > 0
        } catch (_) {
          return false
        }
      },
    })

    cacheHitRate.add(res.headers['X-Cache-Hit'] === 'true')

    if (res.status !== 200) {
      console.error(`[chat] ${res.status}: ${String(res.body).slice(0, 200)}`)
    }
  })
}

// ── /v1/chat/stream (SSE) ─────────────────────────────────────────────────────

function testStream(body) {
  group('POST /v1/chat/stream', () => {
    const start = Date.now()

    // k6 buffers the complete SSE response before returning, so we get the
    // full wall-clock latency including the time to stream all tokens.
    const res = http.post(`${BASE_URL}/v1/chat/stream`, body, {
      headers:      makeHeaders(),
      responseType: 'text',
    })

    const elapsed = Date.now() - start
    streamLatency.add(elapsed)

    check(res, {
      'stream: status 200': (r) => r.status === 200,
      'stream: has data lines': (r) =>
        typeof r.body === 'string' && r.body.includes('data:'),
      'stream: ends with DONE': (r) =>
        typeof r.body === 'string' && r.body.includes('[DONE]'),
    })

    cacheHitRate.add(res.headers['X-Cache-Hit'] === 'true')

    if (res.status !== 200) {
      console.error(`[stream] ${res.status}: ${String(res.body).slice(0, 200)}`)
    }
  })
}
