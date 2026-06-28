<script>
import { onMount } from 'svelte'
import {
  connectWS, loadTree, loadConversation, loadLivePreviews,
  loadControllerHistory,
  currentChat, controllerMessages, wsConnected, sessionMessages
} from './lib/stores.js'
import LivePreview from './components/LivePreview.svelte'
import ChatView from './components/ChatView.svelte'
import TreeView from './components/TreeView.svelte'

let selectedChat = $state({ type: 'workspace', id: 'workspace', title: 'Control Room' })

function onSelectChat(chat) {
  selectedChat = chat
}

onMount(() => {
  connectWS()
  loadTree()
  loadConversation('workspace', 'workspace')
  loadLivePreviews()
  loadControllerHistory()
})
</script>

<div class="app-layout">
  <!-- Left: Live previews -->
  <LivePreview onSelect={onSelectChat} />

  <!-- Center: Chat -->
  <ChatView chat={selectedChat} />

  <!-- Right: Tree navigation -->
  <TreeView onSelect={onSelectChat} />
</div>

<style>
:global(body, html) {
  margin: 0;
  padding: 0;
  background: var(--bg-main);
  color: var(--text-primary);
  font-family: -apple-system, BlinkMacSystemFont, 'Inter', 'Segoe UI', sans-serif;
  -webkit-font-smoothing: antialiased;
  overflow: hidden;
}

.app-layout {
  display: flex;
  height: 100vh;
  width: 100vw;
}

/* CSS variables — dark theme */
:global(:root) {
  --bg-main: #0d0e14;
  --bg-panel: #12131a;
  --border-subtle: rgba(255,255,255,0.06);
  --text-primary: #e4e4e7;
  --text-secondary: #a1a1aa;
  --text-tertiary: #71717a;
  --text-quaternary: #52525b;
  --accent: #7170ff;
  --accent-bright: #9d9bff;
  --success: #27a644;
  --warning: #f59e0b;
  --danger: #ef4444;
  --radius-sm: 4px;
  --radius-pill: 999px;
}

/* Scrollbar styling */
:global(::-webkit-scrollbar) { width: 6px; height: 6px; }
:global(::-webkit-scrollbar-track) { background: transparent; }
:global(::-webkit-scrollbar-thumb) { background: rgba(255,255,255,0.1); border-radius: 3px; }
:global(::-webkit-scrollbar-thumb:hover) { background: rgba(255,255,255,0.15); }
</style>