<script>
import { taskStats, wsConnected } from '../lib/stores.js'
let { title } = $props()
</script>

<div class="topbar">
  <div class="title">{title}</div>
  <div class="stats">
    <div class="stat">
      <div class="dot" class:active={$wsConnected}></div>
      {$taskStats.active || 0} active
    </div>
    <div class="stat">
      <div class="dot open"></div>
      <span class="val">{$taskStats.open || 0}</span> open
    </div>
    <div class="stat">
      <div class="dot review"></div>
      <span class="val">{$taskStats.pending_review || 0}</span> review
    </div>
    <div class="stat">
      <div class="dot approved"></div>
      <span class="val">{$taskStats.approved || 0}</span> approved
    </div>
  </div>
</div>

<style>
.topbar { position: sticky; top: 0; z-index: 100; background: rgba(8,9,10,0.85); backdrop-filter: blur(12px); border-bottom: 1px solid var(--border-subtle); padding: 14px 24px; display: flex; align-items: center; gap: 16px; }
.title { font-size: 18px; font-weight: 600; letter-spacing: -0.28px; }
.stats { display: flex; gap: 20px; margin-left: auto; align-items: center; }
.stat { display: flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 500; color: var(--text-tertiary); }
.val { font-weight: 600; color: var(--text-primary); font-variant-numeric: tabular-nums; }
.dot { width: 8px; height: 8px; border-radius: 50%; }
.dot.open { background: var(--text-tertiary); }
.dot.review { background: var(--warning); }
.dot.approved { background: var(--success-bright); }
.dot.active { background: var(--accent-bright); box-shadow: 0 0 6px rgba(113,112,255,0.5); animation: pulse 2s infinite; }
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.5; } }
</style>