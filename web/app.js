const MAX_POINTS = 120; // keep ~3 minutes at 1.5s refresh
const state = {
  labels: [],
  cpu: [],
  memUsed: [],
  memTotal: 0,
  load1: [], load5: [], load15: [],
  netRx: [], netTx: [], // total bytes across up interfaces
};

function fmtBytes(n) {
  const u = ['B','KB','MB','GB','TB']; let i=0; let x=Number(n||0);
  while (x >= 1024 && i < u.length-1) { x/=1024; i++; }
  return `${x.toFixed(1)} ${u[i]}`;
}
function el(id){ return document.getElementById(id); }

async function fetchMetrics() {
  const res = await fetch('/api/metrics', { cache: 'no-store' });
  if (!res.ok) throw new Error('metrics fetch failed');
  return await res.json();
}

async function fetchHistory() {
  const res = await fetch('/api/history', { cache: 'no-store' });
  if (!res.ok) return [];
  return await res.json();
}

// --- uptime formatter ---
function formatUptime(sec) {
  const s = Math.max(0, Math.floor(sec || 0));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  const rem = s % 60;
  const parts = [];
  if (d) parts.push(`${d}d`);
  if (h) parts.push(`${h}h`);
  if (m) parts.push(`${m}m`);
  parts.push(`${rem}s`);
  return parts.join(" ");
}

// --- one place to write the uptime pill ---
function setUptimeFromMetrics(m) {
  const text = formatUptime(m.uptime_sec);
  if (window.sysdash?.setUptime) {
    window.sysdash.setUptime(text);
  } else {
    const el = document.getElementById("uptime");
    if (el) el.textContent = text;
  }
}

// === charts ===
let cpuChart, memChart, loadChart, netChart;

function mkLineConfig(label, data, yTitle, suggestedMax, color) {
  const c = color || '#3b82f6';
  return {
    type: 'line',
    data: { labels: state.labels, datasets: [{
      label, data, tension: 0.4, fill: true,
      borderColor: c, backgroundColor: c + '20',
      pointRadius: 0, borderWidth: 2
    }] },
    options: {
      responsive: true,
      animation: false,
      scales: {
        x: { ticks: { maxTicksLimit: 6, color: '#6b7280' }, grid: { display: false } },
        y: { beginAtZero: true, suggestedMax, grid: { color: '#ffffff10' }, ticks: { color: '#6b7280' }, title: { display: !!yTitle, text: yTitle, color: '#6b7280' } }
      },
      plugins: { legend: { display: false } }
    }
  };
}

function mkMultiLineConfig(datasets, yTitle, suggestedMax) {
  return {
    type: 'line',
    data: { labels: state.labels, datasets: datasets.map(d => ({
      ...d, tension: 0.4, fill: true, pointRadius: 0, borderWidth: 2, backgroundColor: (d.borderColor || '#3b82f6') + '20'
    })) },
    options: {
      responsive: true, animation: false,
      scales: {
        x: { ticks: { maxTicksLimit: 6, color: '#6b7280' }, grid:{ display:false } },
        y: { beginAtZero: true, suggestedMax, grid: { color: '#ffffff10' }, ticks: { color: '#6b7280' }, title: { display: !!yTitle, text: yTitle, color: '#6b7280' } }
      }
    }
  };
}

function initCharts() {
  cpuChart = new Chart(el('cpuChart'), mkLineConfig('CPU %', state.cpu, '%', undefined, '#f43f5e'));
  memChart = new Chart(el('memChart'), mkLineConfig('Memory Used (MB)', state.memUsed, 'MB', undefined, '#06b6d4'));
  loadChart = new Chart(el('loadChart'), mkMultiLineConfig([
    { label:'1m', data: state.load1, borderColor: '#fbbf24' },
    { label:'5m', data: state.load5, borderColor: '#f97316' },
    { label:'15m', data: state.load15, borderColor: '#ef4444' },
  ], 'load', undefined));
  netChart = new Chart(el('netChart'), mkMultiLineConfig([
    { label:'RX bytes', data: state.netRx, borderColor: '#a855f7' },
    { label:'TX bytes', data: state.netTx, borderColor: '#3b82f6' },
  ], 'bytes'));
}

function pushAndTrim(arr, val) { arr.push(val); if (arr.length > MAX_POINTS) arr.shift(); }

let lastNetTotals = null;

function updateState(m) {
  const ts = new Date(m.timestamp);
  const label = ts.toLocaleTimeString();

  // labels
  pushAndTrim(state.labels, label);

  // cpu
  pushAndTrim(state.cpu, Number(m.cpu_percent?.toFixed(1) || 0));

  // mem
  state.memTotal = (m.mem_total_bytes || 0) / (1024*1024);
  const usedMB = ((m.mem_total_bytes||0) - (m.mem_available_bytes||0)) / (1024*1024);
  pushAndTrim(state.memUsed, usedMB);

  // load
  pushAndTrim(state.load1, Number(m.load1||0));
  pushAndTrim(state.load5, Number(m.load5||0));
  pushAndTrim(state.load15, Number(m.load15||0));

  // network totals (sum of up interfaces)
  let rxSum = 0, txSum = 0;
  (m.net||[]).forEach(n => { if (n.oper_up) { rxSum += n.rx_bytes||0; txSum += n.tx_bytes||0; } });
  if (!lastNetTotals) lastNetTotals = { rx: rxSum, tx: txSum };
  const rxDelta = Math.max(0, rxSum - lastNetTotals.rx);
  const txDelta = Math.max(0, txSum - lastNetTotals.tx);
  lastNetTotals = { rx: rxSum, tx: txSum };
  pushAndTrim(state.netRx, rxDelta);
  pushAndTrim(state.netTx, txDelta);

  // side panels
  el('meta').textContent =
    `${m.hostname} • ${m.os} • ${m.kernel} • ${new Date(m.timestamp).toLocaleString()}`;

  if (typeof m.uptime_sec === "number") setUptimeFromMetrics(m)

  // net table
  el('netTbl').innerHTML =
    `<table><tr><th>IF</th><th>Status</th><th>IPv4</th><th>RX</th><th>TX</th></tr>` +
    (m.net||[]).map(n =>
      `<tr>
        <td class="mono">${n.name}</td>
        <td class="${n.oper_up?'ok':'bad'}">${n.oper_up?'up':'down'}</td>
        <td class="mono">${n.addr_ipv4||''}</td>
        <td>${fmtBytes(n.rx_bytes||0)}</td>
        <td>${fmtBytes(n.tx_bytes||0)}</td>
      </tr>`
    ).join('') + `</table>`;

  // temps
  el('temps').innerHTML =
    (m.temps&&m.temps.length ? m.temps.map(t => `${t.sensor}: <b>${(t.c||0).toFixed(1)}°C</b>`).join('<br/>') : '—');
}

function refreshCharts() {
  cpuChart.update();
  if (state.memTotal) {
    memChart.options.scales.y.title.text = `MB (Total: ${state.memTotal.toFixed(0)})`;
  }
  memChart.update();
  loadChart.update();
  netChart.update();
}

async function tick() {
  try {
    const m = await fetchMetrics();
    updateState(m);
    refreshCharts();
  } catch (e) {
    console.error(e);
  } finally {
    setTimeout(tick, 1500);
  }
}

document.addEventListener('DOMContentLoaded', async () => {
  initCharts();
  try {
    const hist = await fetchHistory();
    if (Array.isArray(hist)) hist.forEach(m => updateState(m));
    refreshCharts();
  } catch (e) { console.error(e); }
  tick();
});
