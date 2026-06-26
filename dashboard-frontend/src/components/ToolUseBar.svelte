<script>
import { getToolTier } from '../lib/stores.js'
let { count = 0, pct = 0, max = 200 } = $props()
let tier = $derived(getToolTier(pct))
</script>

<div class="tool-use">
  <div class="label">
    Tool use
    <span class="value">{count} / {max} ({pct}%)</span>
  </div>
  <div class="bar">
    <div class="fill {tier}" style="width:{pct}%"></div>
  </div>
</div>

<style>
.tool-use { flex: 1; }
.label { font-size: 11px; font-weight: 500; color: var(--text-quaternary); letter-spacing: 0.3px; text-transform: uppercase; margin-bottom: 5px; display: flex; justify-content: space-between; }
.value { color: var(--text-tertiary); font-variant-numeric: tabular-nums; }
.bar { height: 4px; background: rgba(255,255,255,0.06); border-radius: 2px; overflow: hidden; }
.fill { height: 100%; border-radius: 2px; transition: width 0.3s ease; }
.fill.low { background: var(--success-bright); }
.fill.mid { background: var(--warning); }
.fill.high { background: var(--danger); }
</style>