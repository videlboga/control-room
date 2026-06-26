<script>
let { run, onNavigate } = $props()

import Avatar from './Avatar.svelte'
import ToolUseBar from './ToolUseBar.svelte'
import RedoCounter from './RedoCounter.svelte'
import LogStream from './LogStream.svelte'
import { formatTime, formatDuration } from '../lib/stores.js'

const maxTools = 200
let pct = $derived(Math.round((run.tool_use_count || 0) / maxTools * 100))
let isActive = $derived(run.status === 'running')
</script>

<div class="run-card" class:active={isActive}>
  <div class="card-header">
    <Avatar agent={run.agent} {pct} redo={run.redo_index || 0} />
    <div class="titles">
      <div class="agent-line">
        {run.agent}
        <span class="role-badge">{run.agent?.toUpperCase()}</span>
      </div>
      <div class="project">{run.project_title || run.project_id}</div>
      <div class="epic">{run.epic_title || ''}</div>
      <div class="task">{run.task_title || run.task_id}</div>
    </div>
  </div>

  <div class="metrics">
    <ToolUseBar count={run.tool_use_count || 0} {pct} max={maxTools} />
    <RedoCounter redo={run.redo_index || 0} />
  </div>

  <LogStream runId={run.id} />

  <div class="footer">
    <span class="time">Started {formatTime(run.started_at)}</span>
    {#if isActive}
      <span class="duration">{formatDuration(run.started_at)}</span>
    {/if}
    <button type="button" class="link" onclick={() => onNavigate('run-detail', run.id)}>Details →</button>
  </div>
</div>

<style>
.run-card { background: rgba(255,255,255,0.02); border: 1px solid var(--border); border-radius: var(--radius-lg); padding: 20px; display: flex; flex-direction: column; gap: 14px; transition: border-color 0.15s, background 0.15s; position: relative; overflow: hidden; }
.run-card:hover { border-color: rgba(255,255,255,0.12); background: rgba(255,255,255,0.03); }
.run-card::before { content: ''; position: absolute; top: 0; left: 0; right: 0; height: 2px; background: var(--accent); opacity: 0; transition: opacity 0.15s; }
.run-card.active::before { opacity: 1; animation: shimmer 3s infinite; }
@keyframes shimmer { 0%,100% { opacity: 0.3; } 50% { opacity: 1; } }
.card-header { display: flex; align-items: flex-start; gap: 14px; }
.titles { flex: 1; min-width: 0; }
.agent-line { font-size: 13px; font-weight: 600; color: var(--text-secondary); display: flex; align-items: center; gap: 8px; text-transform: capitalize; }
.role-badge { font-size: 10px; font-weight: 500; padding: 1px 7px; border-radius: var(--radius-pill); background: rgba(94,106,210,0.12); color: var(--accent-bright); text-transform: uppercase; letter-spacing: 0.3px; }
.project { font-size: 14px; font-weight: 600; color: var(--text-primary); margin-top: 4px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.epic { font-size: 12px; color: var(--text-quaternary); margin-top: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.task { font-size: 13px; color: var(--text-tertiary); margin-top: 6px; line-height: 1.4; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.metrics { display: flex; align-items: center; gap: 16px; }
.footer { display: flex; align-items: center; gap: 12px; font-size: 12px; color: var(--text-quaternary); }
.time { font-variant-numeric: tabular-nums; }
.link { margin-left: auto; font-size: 12px; font-weight: 500; color: var(--text-tertiary); text-decoration: none; padding: 4px 10px; border-radius: var(--radius-sm); border: 1px solid var(--border-subtle); transition: all 0.12s; background: none; cursor: pointer; font-family: var(--font-sans); }
.link:hover { color: var(--accent-bright); border-color: rgba(94,106,210,0.3); background: rgba(94,106,210,0.05); }
</style>