<script>
import { getToolTier, getRedoTier } from '../lib/stores.js'
let { agent, pct = 0, redo = 0 } = $props()

let toolTier = $derived(getToolTier(pct))
let redoTier = $derived(getRedoTier(redo))
let ringTier = $derived(redoTier === 'high' ? 'high' : toolTier === 'high' ? 'high' : toolTier)
let emoji = $derived({ researcher: '🔬', qa: '✓', engineer: '🛠', pm: '📋' }[agent] || '🤖')
</script>

<div class="avatar">
  <div class="ring {ringTier}"></div>
  <img src="/avatars/{agent}/{toolTier}_{redoTier}.gif" alt={agent} on:error={(e) => e.target.style.display = 'none'} />
  <div class="placeholder">{emoji}</div>
</div>

<style>
.avatar { width: 96px; height: 96px; border-radius: var(--radius-md); background: var(--bg-elevated); border: 1px solid var(--border); display: flex; align-items: center; justify-content: center; font-size: 36px; flex-shrink: 0; position: relative; overflow: hidden; }
.avatar img { width: 100%; height: 100%; object-fit: cover; position: absolute; inset: 0; z-index: 1; }
.placeholder { font-size: 32px; opacity: 0.5; position: relative; z-index: 0; }
.ring { position: absolute; inset: -2px; border-radius: var(--radius-md); border: 2px solid transparent; pointer-events: none; z-index: 2; }
.ring.low { border-color: rgba(39,166,68,0.3); }
.ring.mid { border-color: rgba(245,166,35,0.3); }
.ring.high { border-color: rgba(229,72,77,0.4); }
</style>