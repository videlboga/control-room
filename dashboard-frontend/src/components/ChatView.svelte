<script>
import {
  conversation, currentChat, sendChatMessage,
  apiGet, apiPost, loadConversation, sendWS,
  subscribeConversation, unsubscribeConversation,
  subscribeRun, unsubscribeRun, activeRuns,
  agentStreams, appendToStream, loadProjectAgentHistory,
  agentRunning, setAgentRunningForKey
} from '../lib/stores.js'
import { onMount, onDestroy } from 'svelte'

let { chat } = $props()  // { type, id, title }

let inputText = $state('')
let sending = $state(false)
let scrollContainer = $state(null)
let prevChat = null  // track previous chat for unsub

// Controller state — derived from agentRunning store
let controllerRunning = $derived($agentRunning['workspace'] || false)

// Project agent state — derived from agentRunning store
let projectAgentRunning = $derived(chat?.type === 'project' ? ($agentRunning['project:' + chat.id] || false) : false)

async function checkController() {
  try {
    const s = await apiGet('/controller/status')
    setAgentRunningForKey('workspace', s.running || false)
  } catch (e) { /* ignore */ }
}

async function checkProjectAgent(projectId) {
  try {
    const s = await apiGet(`/project-agent/${projectId}/status`)
    // running=true OR idle=true → can resume via /send
    setAgentRunningForKey('project:' + projectId, s.running || false)
    return s
  } catch (e) { /* ignore */ }
  return null
}

onMount(() => {
  checkController()
  sendWS({ action: 'subscribe', channel: 'controller' })
  subscribeRuns()
  if (chat) {
    loadConversation(chat.type, chat.id)
    subscribeConversation(chat.type, chat.id)
    // Subscribe to agent channel
    if (chat.type === 'workspace') {
      // controller channel already subscribed above
    } else if (chat.type === 'project') {
      sendWS({ action: 'subscribe', channel: `project:${chat.id}` })
      checkProjectAgent(chat.id)
      loadProjectAgentHistory(chat.id)
    }
    if (chat.type === 'task') {
      const run = $activeRuns.find(r => r.task_id === chat.id)
      if (run) subscribeRun(run.id)
    }
  }
})

// Reload conversation + manage subscriptions when chat changes
$effect(() => {
  const c = chat
  if (!c) return

  // Unsubscribe from previous conversation channel
  if (prevChat) {
    unsubscribeConversation(prevChat.type, prevChat.id)
    if (prevChat.type === 'task') {
      const prevRun = $activeRuns.find(r => r.task_id === prevChat.id)
      if (prevRun) unsubscribeRun(prevRun.id)
    }
    if (prevChat.type === 'project') {
      sendWS({ action: 'unsubscribe', channel: `project:${prevChat.id}` })
    }
  }

  // Load new conversation
  loadConversation(c.type, c.id)

  // Subscribe to new conversation channel
  subscribeConversation(c.type, c.id)

  // Subscribe to agent channel
  if (c.type === 'project') {
    sendWS({ action: 'subscribe', channel: `project:${c.id}` })
    checkProjectAgent(c.id)
    loadProjectAgentHistory(c.id)
  }

  // If task with active run, subscribe to run channel for live logs
  if (c.type === 'task') {
    const run = $activeRuns.find(r => r.task_id === c.id)
    if (run) subscribeRun(run.id)
  }

  prevChat = { ...c }
})

onDestroy(() => {
  sendWS({ action: 'unsubscribe', channel: 'controller' })
  if (prevChat) {
    unsubscribeConversation(prevChat.type, prevChat.id)
    if (prevChat.type === 'task') {
      const prevRun = $activeRuns.find(r => r.task_id === prevChat.id)
      if (prevRun) unsubscribeRun(prevRun.id)
    }
  }
})

// Auto-scroll to bottom on new messages
let msgs = $derived($conversation)
// Get agent stream for current chat node
let nodeKey = $derived(
  chat?.type === 'workspace' ? 'workspace' :
  chat?.type === 'project' ? 'project:' + chat.id : null
)
let activeStream = $derived(nodeKey ? ($agentStreams[nodeKey] || []) : [])
// For workspace and project: show agent stream + conversation comments (merged)
// For other types: show conversation only
let activeMsgs = $derived(
  (chat?.type === 'workspace' || chat?.type === 'project')
    ? [...$conversation, ...activeStream].sort((a,b) => (a.timestamp || '').localeCompare(b.timestamp || ''))
    : msgs
)

$effect(() => {
  activeMsgs  // trigger reactivity
  if (scrollContainer) {
    requestAnimationFrame(() => {
      scrollContainer.scrollTop = scrollContainer.scrollHeight
    })
  }
})

async function handleSend() {
  if (!inputText.trim() || sending) return
  sending = true
  const body = inputText.trim()
  inputText = ''

  if (chat?.type === 'workspace') {
    // Workspace chat: message goes to controller agent.
    appendToStream('workspace', { role: 'human', body, timestamp: new Date().toISOString() })
    try {
      if (controllerRunning) {
        await apiPost('/controller/send', { message: body })
      } else {
        await apiPost('/controller/launch', { prompt: body })
        setAgentRunningForKey('workspace', true)
      }
    } catch (e) {
      console.error('controller send:', e)
      // Send failed — controller likely died. Reset state and launch.
      setAgentRunningForKey('workspace', false)
      try {
        await apiPost('/controller/launch', { prompt: body })
        setAgentRunningForKey('workspace', true)
      } catch (e2) {
        appendToStream('workspace', { role: 'system', body: 'Error: ' + e2.message, timestamp: new Date().toISOString() })
      }
    }
  } else if (chat?.type === 'project') {
    // Project chat: message goes to project agent.
    const pKey = 'project:' + chat.id
    appendToStream(pKey, { role: 'human', body, timestamp: new Date().toISOString() })
    try {
      // Check if agent is running OR idle (has session_id for resume)
      const status = await checkProjectAgent(chat.id)
      if (status && (status.running || status.idle)) {
        // Resume existing session
        await apiPost(`/project-agent/${chat.id}/send`, { message: body })
        setAgentRunningForKey(pKey, true)
      } else {
        // Launch new session with compiled context
        await apiPost(`/project-agent/${chat.id}/launch`, { prompt: body })
        setAgentRunningForKey(pKey, true)
      }
    } catch (e) {
      console.error('project agent:', e)
      // If send failed (no session ID, agent died, etc.) — launch new agent
      appendToStream(pKey, { role: 'system', body: 'Launching new agent session...', timestamp: new Date().toISOString() })
      setAgentRunningForKey(pKey, false)
      try {
        await apiPost(`/project-agent/${chat.id}/launch`, { prompt: body })
        setAgentRunningForKey(pKey, true)
      } catch (e2) {
        appendToStream(pKey, { role: 'system', body: 'Error: ' + e2.message, timestamp: new Date().toISOString() })
      }
    }
  } else {
    // Normal conversation (task, run)
    await sendChatMessage(chat.type, chat.id, body)
  }

  sending = false
}

function handleKeydown(e) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    handleSend()
  }
}

function messageClass(msg) {
  if (msg.role === 'human' || msg.author === 'human') return 'msg-human'
  if (msg.role === 'agent' || msg.author === 'agent') return 'msg-agent'
  if (msg.role === 'system') return 'msg-system'
  if (msg.role === 'event') return 'msg-event'
  return 'msg-agent'
}

function messageIcon(msg) {
  const icons = { tool_call: '💻', error: '⚠', step: '⚙️', info: '┊', run_start: '🚀', run_end: '✓' }
  return icons[msg.type] || ''
}

function formatBody(msg) {
  if (msg.type === 'tool_call' && msg.tool) {
    return `🔧 ${msg.tool}: ${msg.body || ''}`
  }
  return msg.body || ''
}

// Group consecutive event/log/output messages into collapsible blocks.
// Human, agent, system messages stay as individual rows.
// Agent responses (⚕ Hermes blocks, or text without ┊/🔧 prefix) are NOT collapsed.
function isToolMessage(msg) {
  // By role — events and logs are always tool messages
  if (msg.role === 'event' || msg.role === 'log') return true
  // By type — explicit tool/log types
  if (msg.type === 'tool_call' || msg.type === 'log_line') return true
  // Agent output — check content. Messages are now multi-line blocks.
  if (msg.role === 'agent' || msg.type === 'output' || msg.type === 'error') {
    const trimmed = (msg.body || '').trim()
    if (!trimmed) return false
    // Hermes response blocks — NOT tool messages, keep visible
    if (trimmed.includes('⚕') || trimmed.includes('Hermes')) return false
    // Prompt echoes and context blocks — NOT tool messages
    if (trimmed.startsWith('Query:') || trimmed.startsWith('##') || trimmed.startsWith('**')) return false
    // Multi-line block: check if FIRST line is a tool prefix
    const firstLine = trimmed.split('\n')[0].trim()
    if (firstLine.startsWith('┊') || firstLine.startsWith('🔧') || firstLine.startsWith('💻') ||
        firstLine.startsWith('$') || firstLine.startsWith('───')) {
      return true
    }
    // Everything else = actual response text — keep visible
    return false
  }
  return false
}

function groupMessages(msgs) {
  const groups = []
  let currentGroup = null

  for (const msg of msgs) {
    if (isToolMessage(msg)) {
      if (!currentGroup) {
        currentGroup = { type: 'tool-group', items: [] }
        groups.push(currentGroup)
      }
      currentGroup.items.push(msg)
    } else {
      currentGroup = null
      groups.push({ type: 'single', item: msg })
    }
  }
  return groups
}

let expandedGroups = $state({})  // group index → expanded

function toggleGroup(idx) {
  expandedGroups[idx] = !expandedGroups[idx]
}

function formatToolLine(msg) {
  if (msg.type === 'tool_call' && msg.tool) {
    return `🔧 ${msg.tool}: ${msg.body || ''}`
  }
  if (msg.body) return msg.body
  return ''
}

// Controller controls
async function stopController() {
  try { await apiPost('/controller/stop', {}) } catch (e) {}
  setAgentRunningForKey('workspace', false)
}

// Workspace chat: first message launches the controller, subsequent messages go via /controller/send
// No dropdown, no Launch button — the chat IS the interface.
</script>

<div class="chat-panel">
  <!-- Chat header -->
  <div class="chat-header">
    <div class="chat-title-info">
      <span class="chat-type-badge">{chat?.type || 'workspace'}</span>
      <span class="chat-title">{chat?.title || 'Control Room'}</span>
    </div>
    {#if chat?.type === 'workspace' && controllerRunning}
      <div class="controller-controls">
        <span class="status-pill running">Running</span>
        <button class="ctrl-btn stop" onclick={stopController}>Stop</button>
      </div>
    {:else if chat?.type === 'project' && projectAgentRunning}
      <div class="controller-controls">
        <span class="status-pill running">Agent</span>
        <button class="ctrl-btn stop" onclick={async () => { try { await apiPost(`/project-agent/${chat.id}/stop`, {}) } catch(e) {} setAgentRunningForKey('project:' + chat.id, false) }}>Stop</button>
      </div>
    {/if}
  </div>

  <!-- Messages -->
  <div class="messages-container" bind:this={scrollContainer}>
    {#each groupMessages(activeMsgs) as group, gi (gi)}
      {#if group.type === 'tool-group'}
        <div class="tool-group" role="button" tabindex="0"
          onclick={() => toggleGroup(gi)}
          onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleGroup(gi) } }}>
          <div class="tool-group-header">
            <span class="tool-group-icon">{expandedGroups[gi] ? '▼' : '▶'}</span>
            <span class="tool-group-summary">
              {group.items.length} tool calls
              {#if group.items.length > 0 && group.items[0].body}
                <span class="tool-group-first">{group.items[0].body.substring(0, 60)}</span>
              {/if}
            </span>
          </div>
          {#if expandedGroups[gi]}
            <div class="tool-group-items">
              {#each group.items as msg, mi (gi + '-' + mi)}
                <div class="tool-item">
                  <span class="tool-item-icon">{messageIcon(msg) || '┊'}</span>
                  <span class="tool-item-text">{formatToolLine(msg)}</span>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {:else}
        {@const msg = group.item}
        <div class="msg-row {messageClass(msg)}">
          <div class="msg-avatar">
            {#if msg.role === 'human' || msg.author === 'human'}👤
            {:else if msg.role === 'system'}⚙️
            {:else}🤖{/if}
          </div>
          <div class="msg-content">
            <div class="msg-meta">
              <span class="msg-author">{msg.author || msg.role}</span>
              {#if msg.timestamp}
                <span class="msg-time">{new Date(msg.timestamp).toLocaleTimeString('ru-RU', {hour:'2-digit', minute:'2-digit'})}</span>
              {/if}
            </div>
            <div class="msg-body">{formatBody(msg)}</div>
          </div>
        </div>
      {/if}
    {/each}

    {#if activeMsgs.length === 0}
      <div class="chat-empty">
        <div class="empty-icon">💬</div>
        <div class="empty-title">No messages yet</div>
        <div class="empty-desc">
          {#if chat?.type === 'workspace'}
            Send a command to the controller agent
          {:else if chat?.type === 'project'}
            Add project context, documentation, or decisions
          {:else}
            Start a conversation with the task agent
          {/if}
        </div>
      </div>
    {/if}
  </div>

  <!-- Input -->
  <div class="chat-input-area">
    <textarea
      class="chat-input"
      bind:value={inputText}
      onkeydown={handleKeydown}
      placeholder={chat?.type === 'workspace' ? 'Type a command…' : chat?.type === 'project' ? 'Ask about the project…' : 'Write a message…'}
      rows="1"
      disabled={sending}
    ></textarea>
    <button class="send-btn" onclick={handleSend} disabled={sending || !inputText.trim()}
      aria-label="Send message">
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2">
        <path d="M2 8l12-5-5 12-2-5z"/>
      </svg>
    </button>
  </div>
</div>

<style>
.chat-panel { display: flex; flex-direction: column; flex: 1; min-width: 0; height: 100vh; background: var(--bg-main); }

.chat-header { display: flex; align-items: center; justify-content: space-between; padding: 12px 20px; border-bottom: 1px solid var(--border-subtle); min-height: 50px; }
.chat-title-info { display: flex; align-items: center; gap: 10px; }
.chat-type-badge { font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; color: var(--accent-bright); background: rgba(94,106,210,0.12); padding: 2px 8px; border-radius: 4px; }
.chat-title { font-size: 15px; font-weight: 600; color: var(--text-primary); }

.controller-controls { display: flex; align-items: center; gap: 8px; }
.status-pill { font-size: 11px; font-weight: 500; padding: 3px 10px; border-radius: 12px; }
.status-pill.running { background: rgba(39,166,68,0.15); color: var(--success); }
.ctrl-btn { font-size: 12px; padding: 4px 12px; border-radius: 4px; border: none; cursor: pointer; font-weight: 500; }
.ctrl-btn.stop { background: rgba(255,100,100,0.15); color: #ff6464; }

.messages-container { flex: 1; overflow-y: auto; padding: 16px 20px; display: flex; flex-direction: column; gap: 2px; }

.msg-row { display: flex; gap: 10px; padding: 6px 0; max-width: 800px; }
.msg-avatar { width: 28px; height: 28px; border-radius: 6px; display: flex; align-items: center; justify-content: center; font-size: 13px; flex-shrink: 0; background: var(--bg-panel); }
.msg-content { min-width: 0; flex: 1; }
.msg-meta { display: flex; align-items: center; gap: 8px; margin-bottom: 2px; }
.msg-author { font-size: 11px; font-weight: 600; color: var(--text-tertiary); }
.msg-time { font-size: 10px; color: var(--text-quaternary); }
.msg-body { font-size: 13px; color: var(--text-secondary); line-height: 1.5; word-break: break-word; white-space: pre-wrap; }

.msg-human .msg-avatar { background: rgba(94,106,210,0.15); }
.msg-human .msg-body { color: var(--text-primary); }
.msg-system .msg-body { color: var(--text-tertiary); font-style: italic; }
.msg-event .msg-body { color: var(--text-tertiary); font-family: monospace; font-size: 12px; }
.msg-event .msg-avatar { background: rgba(255,255,255,0.03); }

.chat-empty { text-align: center; padding: 80px 24px; color: var(--text-quaternary); }
.empty-icon { font-size: 40px; margin-bottom: 16px; opacity: 0.2; }
.empty-title { font-size: 16px; font-weight: 500; color: var(--text-tertiary); margin-bottom: 8px; }
.empty-desc { font-size: 13px; }

.chat-input-area { display: flex; gap: 8px; padding: 12px 20px; border-top: 1px solid var(--border-subtle); background: var(--bg-panel); }
.chat-input { flex: 1; resize: none; border: 1px solid var(--border-subtle); border-radius: 8px; padding: 10px 14px; font-size: 13px; font-family: inherit; color: var(--text-primary); background: var(--bg-main); outline: none; transition: border-color 0.15s; max-height: 120px; }
.chat-input:focus { border-color: var(--accent); }
.chat-input::placeholder { color: var(--text-quaternary); }
.send-btn { width: 36px; height: 36px; border-radius: 8px; border: none; background: var(--accent); color: white; cursor: pointer; display: flex; align-items: center; justify-content: center; flex-shrink: 0; transition: opacity 0.15s; }
.send-btn:disabled { opacity: 0.4; cursor: not-allowed; }
.send-btn:not(:disabled):hover { opacity: 0.85; }

/* Tool group — collapsible block of consecutive tool/log messages */
.tool-group { margin: 4px 0; max-width: 800px; border: 1px solid var(--border-subtle); border-radius: 6px; overflow: visible; }
.tool-group-header { display: flex; align-items: center; gap: 8px; padding: 8px 10px; background: rgba(255,255,255,0.02); cursor: pointer; user-select: none; min-height: 32px; }
.tool-group-header:hover { background: rgba(255,255,255,0.04); }
.tool-group-icon { font-size: 10px; color: var(--text-quaternary); width: 12px; flex-shrink: 0; }
.tool-group-summary { font-size: 11px; color: var(--text-tertiary); font-family: monospace; }
.tool-group-first { color: var(--text-quaternary); margin-left: 6px; }
.tool-group-items { padding: 4px 0; background: rgba(0,0,0,0.15); }
.tool-item { display: flex; gap: 6px; padding: 3px 10px; font-size: 11px; font-family: monospace; color: var(--text-tertiary); line-height: 1.4; }
.tool-item-icon { flex-shrink: 0; opacity: 0.6; }
.tool-item-text { white-space: pre-wrap; word-break: break-word; }
</style>