<script>
import { livePreviews, currentChat, loadConversation, loadLivePreviews } from '../lib/stores.js'
import { onMount, onDestroy } from 'svelte'

let { onSelect } = $props()

let previews = $derived($livePreviews)

// Auto-refresh previews
let timer
onMount(() => {
  loadLivePreviews()
  timer = setInterval(loadLivePreviews, 3000)
})
onDestroy(() => { if (timer) clearInterval(timer) })

async function selectPreview(preview) {
  if (preview.type === 'task') {
    await loadConversation('task', preview.id)
    onSelect?.({ type: 'task', id: preview.id, title: preview.title })
  } else if (preview.type === 'workspace') {
    await loadConversation('workspace', 'workspace')
    onSelect?.({ type: 'workspace', id: 'workspace', title: 'Control Room' })
  }
}

function isActive(preview) {
  return $currentChat.type === preview.type &&
    (preview.type === 'workspace' || $currentChat.id === preview.id)
}

function formatTail(tailLines) {
  if (!tailLines || tailLines.length === 0) return ''
  return tailLines.slice(-3).join('\n')
}
</script>

<div class="live-panel">
  <div class="live-header">
    <span class="live-title">Live</span>
    <span class="live-count">{previews.length}</span>
  </div>

  <div class="live-list">
    {#each previews as p (p.id)}
      <div class="preview-card" role="button" tabindex="0"
        class:active={isActive(p)}
        onclick={() => selectPreview(p)}
        onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectPreview(p) } }}>
        <div class="preview-top">
          <span class="preview-type">{p.type}</span>
          <span class="streaming-indicator"></span>
        </div>
        <div class="preview-title">{p.title}</div>
        {#if p.agent}
          <div class="preview-meta">{p.agent} · {p.step || p.status}</div>
        {/if}
        {#if p.tail_lines && p.tail_lines.length > 0}
          <pre class="preview-tail">{formatTail(p.tail_lines)}</pre>
        {/if}
      </div>
    {/each}

    {#if previews.length === 0}
      <div class="live-empty">
        <div class="empty-icon">⏳</div>
        <div class="empty-desc">No active streams</div>
      </div>
    {/if}
  </div>
</div>

<style>
.live-panel { width: 240px; min-width: 240px; background: var(--bg-panel); border-right: 1px solid var(--border-subtle); display: flex; flex-direction: column; height: 100vh; overflow: hidden; }
.live-header { padding: 16px 14px 12px; border-bottom: 1px solid var(--border-subtle); display: flex; align-items: center; gap: 8px; }
.live-title { font-size: 12px; font-weight: 600; color: var(--text-tertiary); letter-spacing: 0.5px; text-transform: uppercase; }
.live-count { font-size: 10px; font-weight: 500; color: var(--text-quaternary); background: rgba(255,255,255,0.06); padding: 1px 7px; border-radius: 8px; }

.live-list { flex: 1; overflow-y: auto; padding: 8px; display: flex; flex-direction: column; gap: 6px; }

.preview-card { background: rgba(255,255,255,0.02); border: 1px solid var(--border-subtle); border-radius: 6px; padding: 10px 12px; cursor: pointer; transition: all 0.12s; }
.preview-card:hover { background: rgba(255,255,255,0.04); border-color: rgba(255,255,255,0.08); }
.preview-card.active { border-color: var(--accent); background: rgba(94,106,210,0.08); }

.preview-top { display: flex; align-items: center; justify-content: space-between; margin-bottom: 4px; }
.preview-type { font-size: 9px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; color: var(--text-quaternary); }
.streaming-indicator { width: 6px; height: 6px; border-radius: 50%; background: var(--success); box-shadow: 0 0 6px rgba(39,166,68,0.5); animation: pulse 1.5s infinite; }
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.3; } }

.preview-title { font-size: 12px; font-weight: 500; color: var(--text-secondary); margin-bottom: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.preview-meta { font-size: 10px; color: var(--text-quaternary); margin-bottom: 4px; }
.preview-tail { font-size: 10px; color: var(--text-tertiary); font-family: monospace; line-height: 1.4; max-height: 48px; overflow: hidden; margin: 0; white-space: pre-wrap; word-break: break-word; opacity: 0.7; }

.live-empty { text-align: center; padding: 60px 16px; color: var(--text-quaternary); }
.empty-icon { font-size: 32px; margin-bottom: 12px; opacity: 0.2; }
.empty-desc { font-size: 12px; }

@media (max-width: 1024px) { .live-panel { width: 180px; min-width: 180px; } }
</style>