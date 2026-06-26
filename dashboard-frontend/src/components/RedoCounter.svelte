<script>
import { getRedoTier } from '../lib/stores.js'
let { redo = 0 } = $props()
let tier = $derived(getRedoTier(redo))
</script>

<div class="redo-section">
  <div class="label">Redo</div>
  <div class="badge {tier}">
    <svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2">
      <path d="M2 8a6 6 0 0 1 10.5-3.5M14 8a6 6 0 0 1-10.5 3.5"/>
      <path d="M12 2v3h-3M4 14v-3h3"/>
    </svg>
    {redo}
  </div>
</div>

<style>
.redo-section { flex-shrink: 0; }
.label { font-size: 11px; font-weight: 500; color: var(--text-quaternary); letter-spacing: 0.3px; text-transform: uppercase; margin-bottom: 5px; }
.badge { display: flex; align-items: center; gap: 5px; padding: 4px 10px; border-radius: var(--radius-pill); font-size: 12px; font-weight: 600; font-variant-numeric: tabular-nums; }
.badge.low { background: rgba(39,166,68,0.1); color: var(--success-bright); border: 1px solid rgba(39,166,68,0.15); }
.badge.mid { background: rgba(245,166,35,0.1); color: var(--warning); border: 1px solid rgba(245,166,35,0.15); }
.badge.high { background: rgba(229,72,77,0.1); color: var(--danger); border: 1px solid rgba(229,72,77,0.15); }
</style>