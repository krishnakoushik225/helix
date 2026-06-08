import { useEffect, useState } from 'react'
import { fetchStats, type StatsResponse } from './api'
import CacheHitRate from './components/CacheHitRate'
import CostTracker from './components/CostTracker'
import LatencyChart from './components/LatencyChart'
import ProviderStatus from './components/ProviderStatus'
import RequestFeed from './components/RequestFeed'

const POLL_MS = 3_000

// ── Token gate ─────────────────────────────────────────────────────────────

function TokenGate({ onConnect }: { onConnect: (t: string) => void }) {
  const [value, setValue] = useState('')

  const submit = () => {
    const t = value.trim()
    if (!t) return
    localStorage.setItem('helix_token', t)
    onConnect(t)
  }

  return (
    <div className="min-h-screen bg-gray-950 flex items-center justify-center">
      <div className="bg-gray-800 p-8 rounded-2xl shadow-2xl w-full max-w-sm border border-gray-700">
        <h1 className="text-2xl font-bold text-white mb-1">HELIX</h1>
        <p className="text-gray-400 text-sm mb-6">Enter your JWT token to connect</p>
        <input
          type="password"
          className="w-full bg-gray-700 border border-gray-600 text-white text-sm font-mono px-4 py-2.5 rounded-lg mb-4 focus:outline-none focus:ring-2 focus:ring-indigo-500 placeholder-gray-500"
          placeholder="eyJ…"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && submit()}
        />
        <button
          onClick={submit}
          className="w-full bg-indigo-600 hover:bg-indigo-500 text-white font-semibold py-2.5 rounded-lg transition-colors"
        >
          Connect
        </button>
      </div>
    </div>
  )
}

// ── Main dashboard ──────────────────────────────────────────────────────────

export default function App() {
  const [token, setToken] = useState<string>(
    localStorage.getItem('helix_token') ?? '',
  )
  const [stats, setStats] = useState<StatsResponse | null>(null)
  const [connected, setConnected] = useState(false)
  const [lastError, setLastError] = useState('')

  useEffect(() => {
    if (!token) return

    let cancelled = false
    const poll = async () => {
      try {
        const data = await fetchStats(token)
        if (!cancelled) {
          setStats(data)
          setConnected(true)
          setLastError('')
        }
      } catch (e) {
        if (!cancelled) {
          setConnected(false)
          setLastError(String(e))
        }
      }
    }

    poll()
    const id = setInterval(poll, POLL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [token])

  if (!token) {
    return <TokenGate onConnect={setToken} />
  }

  const signOut = () => {
    localStorage.removeItem('helix_token')
    setToken('')
    setStats(null)
    setConnected(false)
  }

  return (
    <div className="min-h-screen bg-gray-950 text-white flex flex-col">
      {/* ── Header ── */}
      <header className="border-b border-gray-800 px-6 py-3 flex items-center justify-between shrink-0">
        <div className="flex items-center gap-3">
          <span className="text-lg font-bold tracking-wide text-transparent bg-clip-text bg-gradient-to-r from-indigo-400 to-purple-400">
            HELIX
          </span>
          <span className="text-gray-600 text-sm hidden sm:block">AI Inference Gateway</span>
        </div>

        <div className="flex items-center gap-4">
          <div
            className={`flex items-center gap-1.5 text-xs ${
              connected ? 'text-green-400' : 'text-red-400'
            }`}
          >
            <span
              className={`w-2 h-2 rounded-full ${
                connected ? 'bg-green-400 animate-pulse' : 'bg-red-400'
              }`}
            />
            {connected ? 'Live' : 'Disconnected'}
          </div>
          <button
            onClick={signOut}
            className="text-gray-500 hover:text-gray-300 text-xs transition-colors"
          >
            Sign out
          </button>
        </div>
      </header>

      {/* ── Error banner ── */}
      {lastError && (
        <div className="bg-red-950/60 border-b border-red-900 text-red-300 px-6 py-2 text-xs">
          {lastError}
        </div>
      )}

      {/* ── Two-column layout ── */}
      <main className="flex-1 p-6 grid grid-cols-1 lg:grid-cols-3 gap-6 min-h-0">
        {/* Left column — charts + feed (2/3) */}
        <div className="lg:col-span-2 flex flex-col gap-6">
          <LatencyChart token={token} />
          <RequestFeed stats={stats} />
        </div>

        {/* Right column — KPIs + provider status (1/3) */}
        <div className="flex flex-col gap-6">
          <CacheHitRate stats={stats} />
          <CostTracker stats={stats} />
          <ProviderStatus token={token} stats={stats} />
        </div>
      </main>
    </div>
  )
}
