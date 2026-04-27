function fmt(ts) {
  const d = new Date(ts);
  return d.toTimeString().slice(0,8);
}

function renderEmpty() {
  document.getElementById('main').innerHTML = `
    <div class="empty">No events yet.<br>Open Gmail and start working.</div>
  `;
}

function renderData(events) {
  if (!events || events.length === 0) { renderEmpty(); return; }

  // Count actions
  const counts = {};
  events.forEach(e => {
    counts[e.action] = (counts[e.action] || 0) + 1;
  });

  const sorted = Object.entries(counts).sort((a,b) => b[1]-a[1]);
  const maxCount = sorted[0]?.[1] || 1;
  const top = sorted.slice(0, 7);

  // Sessions
  const sessions = new Set(events.map(e => e.session)).size;
  document.getElementById('session-count').textContent = `${sessions} session${sessions !== 1 ? 's' : ''}`;

  // Recent events (last 15)
  const recent = events.slice(-15).reverse();

  document.getElementById('main').innerHTML = `
    <div class="stats-grid">
      <div class="stat">
        <div class="stat-label">Total Events</div>
        <div class="stat-value">${events.length}</div>
      </div>
      <div class="stat">
        <div class="stat-label">Action Types</div>
        <div class="stat-value">${Object.keys(counts).length}</div>
      </div>
    </div>

    <div class="top-actions">
      <h2>Top Actions</h2>
      <ul class="action-list">
        ${top.map(([action, count]) => `
          <li class="action-item">
            <span class="action-name">${action}</span>
            <div class="bar-bg"><div class="bar-fill" style="width:${Math.round(count/maxCount*100)}%"></div></div>
            <span class="action-count">${count}</span>
          </li>
        `).join('')}
      </ul>
    </div>

    <div class="recent">
      <h2>Recent</h2>
      ${recent.map(e => `
        <div class="event-line">
          <span class="ts">${fmt(e.ts)}</span>
          <span class="act">${e.action}</span>
          <span class="ctx">${e.view?.view || ''} ${e.method ? '· ' + e.method : ''}</span>
        </div>
      `).join('')}
    </div>

    <div class="actions-row">
      <button id="btn-export">⬇ Export JSON</button>
      <button id="btn-analyze">⚡ Analyze</button>
      <button id="btn-clear">✕</button>
    </div>
  `;

  document.getElementById('btn-export').addEventListener('click', () => exportJSON(events));
  document.getElementById('btn-clear').addEventListener('click', clearEvents);
  document.getElementById('btn-analyze').addEventListener('click', () => analyzeEvents(events, counts));
}

function exportJSON(events) {
  const blob = new Blob([JSON.stringify({ exported: new Date().toISOString(), events }, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `gmail-log-${Date.now()}.json`;
  a.click();
  URL.revokeObjectURL(url);
}

function clearEvents() {
  if (!confirm('Clear all logged events?')) return;
  chrome.runtime.sendMessage({ type: 'CLEAR_EVENTS' }, () => load());
}

function analyzeEvents(events, counts) {
  // Build workflow sequences: group events into sessions and find common patterns
  const sessions = {};
  events.forEach(e => {
    if (!sessions[e.session]) sessions[e.session] = [];
    sessions[e.session].push(e.action);
  });

  // Find bigrams (action pairs)
  const bigrams = {};
  Object.values(sessions).forEach(seq => {
    for (let i = 0; i < seq.length - 1; i++) {
      const pair = seq[i] + ' → ' + seq[i+1];
      bigrams[pair] = (bigrams[pair] || 0) + 1;
    }
  });

  const topBigrams = Object.entries(bigrams)
    .sort((a,b) => b[1]-a[1])
    .slice(0, 8);

  const report = {
    summary: {
      total_events: events.length,
      unique_actions: Object.keys(counts).length,
      sessions: Object.keys(sessions).length,
      date_range: {
        first: new Date(events[0]?.ts).toISOString(),
        last: new Date(events[events.length-1]?.ts).toISOString(),
      }
    },
    top_actions: Object.fromEntries(
      Object.entries(counts).sort((a,b) => b[1]-a[1]).slice(0, 20)
    ),
    top_workflows: Object.fromEntries(topBigrams),
    keyboard_vs_click: {
      keyboard: events.filter(e => e.method === 'keyboard').length,
      click: events.filter(e => e.method === 'click').length,
    },
    views_visited: [...new Set(events.map(e => e.view?.view).filter(Boolean))],
  };

  const blob = new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `gmail-analysis-${Date.now()}.json`;
  a.click();
  URL.revokeObjectURL(url);
}

function load() {
  chrome.runtime.sendMessage({ type: 'GET_EVENTS' }, (res) => {
    renderData(res?.events || []);
  });
}

load();
