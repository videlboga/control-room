<script>
import { runLogs } from '../lib/stores.js'
let { runId } = $props()
let logs = $derived($runLogs[runId] || [])
</script>

<div class="log">
  {#each logs as entry}
    <div class="log-line">
      <span class="time">[{entry.timestamp?.slice(11, 16) || ''}]</span>
      <span class="icon">{entry.icon || '┊'}</span>
      <span class="cmd">{entry.line || ''}</span>
    </div>
  {/each}
  <div class="cursor"></div>
</div>

<style>
.log { background: var(--bg-panel); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); padding: 10px 12px; font-family: var(--font-mono); font-size: 12px; line-height: 1.6; height: 100px; overflow: hidden; position: relative; }
.log::after { content: ''; position: absolute; bottom: 0; left: 0; right: 0; height: 24px; background: linear-gradient(transparent, var(--bg-panel)); pointer-events: none; }
.log-line { color: var(--text-tertiary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; animation: logIn 0.2s ease; }
@keyframes logIn { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: translateY(0); } }
.time { color: var(--text-quaternary); }
.icon { color: var(--accent-bright); margin: 0 2px; }
.cmd { color: var(--text-secondary); }
.cursor { display: inline-block; width: 7px; height: 13px; background: var(--accent-bright); animation: blink 1s step-end infinite; vertical-align: text-bottom; }
@keyframes blink { 0%, 50% { opacity: 1; } 51%, 100% { opacity: 0; } }
</style>