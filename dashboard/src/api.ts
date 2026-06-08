/// <reference types="vite/client" />

export const API_BASE =
  (import.meta.env.VITE_API_URL as string | undefined)?.replace(/\/$/, '') ||
  'http://localhost:8080'

// ---- wire types ----

export interface ProviderBreakdown {
  requests: number
  cost_usd: number
  avg_latency_ms: number
}

export interface StatsResponse {
  total_requests: number
  cache_hit_rate: number
  avg_latency_ms: number
  cost_today_usd: number
  provider_breakdown: Record<string, ProviderBreakdown>
}

export interface ProviderHealth {
  name: string
  state: 'closed' | 'open' | 'half_open' | 'unknown'
}

// ---- fetchers ----

export async function fetchStats(token: string): Promise<StatsResponse> {
  const res = await fetch(`${API_BASE}/v1/stats`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`/v1/stats returned HTTP ${res.status}`)
  return res.json() as Promise<StatsResponse>
}

export async function fetchProviders(token: string): Promise<ProviderHealth[]> {
  const res = await fetch(`${API_BASE}/v1/providers`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`/v1/providers returned HTTP ${res.status}`)
  return res.json() as Promise<ProviderHealth[]>
}
