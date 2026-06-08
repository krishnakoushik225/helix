import { useEffect, useRef, useState } from 'react'
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
  ResponsiveContainer,
} from 'recharts'
import { fetchStats, type StatsResponse } from '../api'

interface Props {
  token: string
}

interface DataPoint {
  time: string
  [provider: string]: string | number
}

const POLL_INTERVAL_MS = 10_000
const MAX_POINTS = 36 // 6 minutes of 10s polling
const PROVIDER_COLORS: Record<string, string> = {
  ollama: '#818cf8',
  openai: '#34d399',
  anthropic: '#f472b6',
}
const FALLBACK_COLORS = ['#fb923c', '#38bdf8', '#a78bfa', '#fbbf24']

function nextColor(idx: number): string {
  return FALLBACK_COLORS[idx % FALLBACK_COLORS.length]
}

function formatTime(d: Date): string {
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export default function LatencyChart({ token }: Props) {
  const [series, setSeries] = useState<DataPoint[]>([])
  const [providers, setProviders] = useState<string[]>([])
  const [error, setError] = useState('')
  const providerColorsRef = useRef<Record<string, string>>({})

  useEffect(() => {
    let cancelled = false

    const poll = async () => {
      try {
        const data: StatsResponse = await fetchStats(token)
        if (cancelled) return
        setError('')

        const now = new Date()
        const point: DataPoint = { time: formatTime(now) }

        const seenProviders = Object.keys(data.provider_breakdown)
        for (const p of seenProviders) {
          point[p] = Math.round(data.provider_breakdown[p].avg_latency_ms)
          if (!providerColorsRef.current[p]) {
            const idx = Object.keys(providerColorsRef.current).length
            providerColorsRef.current[p] = PROVIDER_COLORS[p] ?? nextColor(idx)
          }
        }

        setProviders(seenProviders)
        setSeries((prev) => [...prev, point].slice(-MAX_POINTS))
      } catch (e) {
        if (!cancelled) setError(String(e))
      }
    }

    poll()
    const id = setInterval(poll, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [token])

  return (
    <div className="bg-gray-800 rounded-xl p-6 border border-gray-700">
      <div className="flex items-center justify-between mb-6">
        <p className="text-xs font-medium text-gray-400 uppercase tracking-widest">
          Avg Latency by Provider
        </p>
        <span className="text-xs text-gray-500">10 s interval · last {MAX_POINTS} points</span>
      </div>

      {error && (
        <p className="text-red-400 text-xs mb-4">{error}</p>
      )}

      {series.length === 0 ? (
        <div className="h-48 flex items-center justify-center text-gray-500 text-sm">
          Collecting data…
        </div>
      ) : (
        <ResponsiveContainer width="100%" height={200}>
          <LineChart data={series} margin={{ top: 4, right: 8, left: 0, bottom: 4 }}>
            <CartesianGrid strokeDasharray="3 3" stroke="#374151" />
            <XAxis
              dataKey="time"
              tick={{ fill: '#6b7280', fontSize: 10 }}
              tickLine={false}
              interval="preserveStartEnd"
            />
            <YAxis
              tick={{ fill: '#6b7280', fontSize: 10 }}
              tickLine={false}
              axisLine={false}
              unit="ms"
              width={55}
            />
            <Tooltip
              contentStyle={{
                backgroundColor: '#1f2937',
                border: '1px solid #374151',
                borderRadius: 8,
                fontSize: 12,
              }}
              labelStyle={{ color: '#9ca3af' }}
              formatter={(val: number | string) =>
                typeof val === 'number' ? [`${val}ms`] : [val]
              }
            />
            <Legend
              wrapperStyle={{ fontSize: 12, color: '#9ca3af' }}
            />
            {providers.map((p) => (
              <Line
                key={p}
                type="monotone"
                dataKey={p}
                stroke={providerColorsRef.current[p]}
                strokeWidth={2}
                dot={false}
                activeDot={{ r: 4 }}
              />
            ))}
          </LineChart>
        </ResponsiveContainer>
      )}
    </div>
  )
}
