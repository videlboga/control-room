<script>
import { tree, currentChat, loadConversation, loadTree, subscribeTree, sendWS } from '../lib/stores.js'
import { onMount, onDestroy } from 'svelte'

let { onSelect } = $props()

let expanded = $state({ workspace: true })

async function selectNode(type, id, title) {
  await loadConversation(type, id)
  onSelect?.({ type, id, title })
}

function toggleExpand(id) {
  expanded[id] = !expanded[id]
}

function statusColor(status) {
  const colors = {
    open: 'var(--text-quaternary)',
    in_progress: 'var(--accent)',
    pending_review: 'var(--warning)',
    approved: 'var(--success)',
    rejected: 'var(--danger)',
    done: 'var(--success)',
  }
  return colors[status] || 'var(--text-quaternary)'
}

let treeData = $derived($tree)

// Subscribe to tree updates via WS (tree_update events trigger loadTree in stores.js)
onMount(() => {
  loadTree()
  subscribeTree()
})

onDestroy(() => {
  sendWS({ action: 'unsubscribe', channel: 'tree' })
})
</script>

<div class="tree-panel">
  <div class="tree-header">
    <span class="tree-title">Workspace</span>
    <button class="refresh-btn" onclick={loadTree} title="Refresh">
      <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2">
        <path d="M2 8a6 6 0 0 1 10.5-3.5M14 8a6 6 0 0 1-10.5 3.5"/>
        <path d="M12 2v3h-3M4 14v-3h3"/>
      </svg>
    </button>
  </div>

  {#if treeData}
    <div class="tree-list">
      <!-- Workspace root -->
      <div class="tree-node workspace-node">
        <div class="node-row root-row" role="button" tabindex="0"
          class:active={$currentChat.type === 'workspace'}
          onclick={() => selectNode('workspace', 'workspace', 'Control Room')}
          onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectNode('workspace', 'workspace', 'Control Room') } }}>
          <button class="expand-btn" onclick={(e) => { e.stopPropagation(); toggleExpand('workspace') }}
            aria-label="Expand workspace">
            {#if expanded.workspace}▼{:else}▶{/if}
          </button>
          <span class="node-icon">◈</span>
          <span class="node-label">Control Room</span>
        </div>

        {#if expanded.workspace && treeData.children}
          {#each treeData.children as proj (proj.id)}
            <div class="tree-node project-node">
              <div class="node-row project-row" role="button" tabindex="0"
                class:active={$currentChat.type === 'project' && $currentChat.id === proj.id}
                onclick={() => selectNode('project', proj.id, proj.title)}
                onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectNode('project', proj.id, proj.title) } }}>
                <button class="expand-btn" onclick={(e) => { e.stopPropagation(); toggleExpand(proj.id) }}
                  aria-label="Expand {proj.title}">
                  {#if expanded[proj.id]}▼{:else}▶{/if}
                </button>
                <span class="node-icon">◇</span>
                <span class="node-label">{proj.title}</span>
                {#if proj.children}
                  <span class="child-count">{proj.children.length}</span>
                {/if}
              </div>

              {#if expanded[proj.id] && proj.children}
                {#each proj.children as task (task.id)}
                  <div class="tree-node task-node">
                    <div class="node-row task-row" role="button" tabindex="0"
                      class:active={$currentChat.type === 'task' && $currentChat.id === task.id}
                      onclick={() => selectNode('task', task.id, task.title)}
                      onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectNode('task', task.id, task.title) } }}>
                      <span class="task-spacer"></span>
                      <span class="node-icon" style="color: {statusColor(task.status)}">●</span>
                      <span class="node-label task-label">{task.title}</span>
                      {#if task.streaming}
                        <span class="streaming-dot" title="Live"></span>
                      {/if}
                      {#if task.redo_index > 0}
                        <span class="redo-badge">{task.redo_index}</span>
                      {/if}
                    </div>
                  </div>
                {/each}
              {/if}
            </div>
          {/each}
        {/if}
      </div>
    </div>
  {:else}
    <div class="tree-empty">Loading…</div>
  {/if}
</div>

<style>
.tree-panel { width: 260px; min-width: 260px; background: var(--bg-panel); border-left: 1px solid var(--border-subtle); display: flex; flex-direction: column; height: 100vh; overflow: hidden; }
.tree-header { padding: 16px 14px 12px; border-bottom: 1px solid var(--border-subtle); display: flex; align-items: center; justify-content: space-between; }
.tree-title { font-size: 12px; font-weight: 600; color: var(--text-tertiary); letter-spacing: 0.5px; text-transform: uppercase; }
.refresh-btn { background: none; border: none; color: var(--text-quaternary); cursor: pointer; padding: 4px; border-radius: 4px; }
.refresh-btn:hover { color: var(--text-secondary); background: rgba(255,255,255,0.06); }
.tree-list { flex: 1; overflow-y: auto; padding: 8px 0; }

.tree-node { user-select: none; }

.node-row { display: flex; align-items: center; gap: 6px; padding: 4px 10px; cursor: pointer; border-radius: 4px; margin: 0 6px; font-size: 13px; transition: background 0.1s; }
.node-row:hover { background: rgba(255,255,255,0.04); }
.node-row.active { background: rgba(94,106,210,0.15); color: var(--accent-bright); }

.root-row { font-weight: 600; color: var(--text-primary); }
.project-row { color: var(--text-secondary); padding-left: 20px; }
.task-row { padding-left: 40px; color: var(--text-tertiary); font-size: 12px; }

.expand-btn { background: none; border: none; color: var(--text-quaternary); cursor: pointer; font-size: 9px; padding: 0; width: 14px; line-height: 1; }
.node-icon { font-size: 11px; opacity: 0.8; flex-shrink: 0; }
.node-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 1; }
.task-label { font-size: 12px; }

.child-count { font-size: 10px; color: var(--text-quaternary); background: rgba(255,255,255,0.06); padding: 1px 6px; border-radius: 8px; }

.streaming-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--success); box-shadow: 0 0 6px rgba(39,166,68,0.5); animation: pulse 1.5s infinite; flex-shrink: 0; }
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.3; } }

.redo-badge { font-size: 9px; font-weight: 600; background: rgba(255,100,100,0.15); color: #ff6464; padding: 1px 5px; border-radius: 8px; }

.task-spacer { width: 14px; flex-shrink: 0; }

.tree-empty { padding: 24px; text-align: center; color: var(--text-quaternary); font-size: 13px; }

@media (max-width: 1024px) { .tree-panel { width: 200px; min-width: 200px; } }
</style>