package daemon

import (
	"io"
	"net/http"
)

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>SmallCloud Operator Console</title>
<style>
  :root {
    color-scheme: dark;
    --bg: #0b0f17;
    --panel: #131a26;
    --border: #243044;
    --text: #e6edf7;
    --muted: #8b9bb4;
    --accent: #3b82f6;
    --green: #22c55e;
    --blue: #38bdf8;
    --red: #ef4444;
    --amber: #f59e0b;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: var(--bg);
    color: var(--text);
    line-height: 1.5;
  }
  .container {
    max-width: 1120px;
    margin: 0 auto;
    padding: 24px 20px 48px;
  }
  header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding-bottom: 20px;
    border-bottom: 1px solid var(--border);
    margin-bottom: 24px;
  }
  .brand h1 {
    margin: 0;
    font-size: 1.5rem;
    letter-spacing: -0.02em;
  }
  .brand p {
    margin: 2px 0 0;
    color: var(--muted);
    font-size: 0.875rem;
  }
  .controls {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
  }
  input[type="password"], input[type="text"] {
    background: var(--panel);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 8px 12px;
    border-radius: 6px;
    font-size: 0.875rem;
    min-width: 220px;
  }
  button {
    background: var(--accent);
    color: #fff;
    border: none;
    padding: 8px 14px;
    border-radius: 6px;
    font-size: 0.875rem;
    font-weight: 600;
    cursor: pointer;
  }
  button:hover { opacity: 0.92; }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  .banner {
    display: none;
    padding: 10px 14px;
    border-radius: 6px;
    margin-bottom: 20px;
    font-size: 0.875rem;
    background: rgba(239, 68, 68, 0.14);
    border: 1px solid rgba(239, 68, 68, 0.4);
    color: #fecaca;
  }
  .banner.visible { display: block; }
  section {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 18px 20px;
    margin-bottom: 24px;
  }
  .section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 14px;
  }
  .section-head h2 {
    margin: 0;
    font-size: 1.05rem;
  }
  .meta {
    color: var(--muted);
    font-size: 0.8rem;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.875rem;
  }
  th, td {
    text-align: left;
    padding: 10px 8px;
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  th {
    color: var(--muted);
    font-weight: 600;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  tr:last-child td { border-bottom: none; }
  .badge {
    display: inline-block;
    padding: 2px 8px;
    border-radius: 999px;
    font-size: 0.75rem;
    font-weight: 600;
    text-transform: uppercase;
  }
  .badge-blue { background: rgba(56, 189, 248, 0.18); color: var(--blue); }
  .badge-green { background: rgba(34, 197, 94, 0.18); color: var(--green); }
  .badge-red { background: rgba(239, 68, 68, 0.18); color: var(--red); }
  .badge-muted { background: rgba(139, 155, 180, 0.18); color: var(--muted); }
  .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.82rem; }
  .empty {
    color: var(--muted);
    font-size: 0.875rem;
    padding: 12px 0;
  }
</style>
</head>
<body>
<div class="container">
  <header>
    <div class="brand">
      <h1>SmallCloud</h1>
      <p>Operator Dashboard</p>
    </div>
    <div class="controls">
      <input id="token-input" type="password" placeholder="Optional Bearer token" aria-label="Bearer token">
      <button id="refresh-btn" type="button">Refresh</button>
    </div>
  </header>

  <div id="error-banner" class="banner" role="alert"></div>

  <section>
    <div class="section-head">
      <h2>Apps</h2>
      <span id="apps-count" class="meta">0 apps</span>
    </div>
    <div id="apps-container"></div>
  </section>

  <section>
    <div class="section-head">
      <h2>Recent Audit Events</h2>
      <span id="events-count" class="meta">0 events</span>
    </div>
    <div id="events-container"></div>
  </section>
</div>

<script>
(function() {
  var TOKEN_KEY = "smallcloud_bearer_token";
  var tokenInput = document.getElementById("token-input");
  var refreshBtn = document.getElementById("refresh-btn");
  var errorBanner = document.getElementById("error-banner");
  var appsContainer = document.getElementById("apps-container");
  var eventsContainer = document.getElementById("events-container");
  var appsCount = document.getElementById("apps-count");
  var eventsCount = document.getElementById("events-count");

  var savedToken = sessionStorage.getItem(TOKEN_KEY) || "";
  tokenInput.value = savedToken;

  tokenInput.addEventListener("input", function() {
    var val = tokenInput.value.trim();
    if (val) {
      sessionStorage.setItem(TOKEN_KEY, val);
    } else {
      sessionStorage.removeItem(TOKEN_KEY);
    }
  });

  function authHeaders() {
    var token = (sessionStorage.getItem(TOKEN_KEY) || "").trim();
    if (!token) {
      return {};
    }
    return { "Authorization": "Bearer " + token };
  }

  function showError(msg) {
    if (!msg) {
      errorBanner.textContent = "";
      errorBanner.classList.remove("visible");
      return;
    }
    errorBanner.textContent = msg;
    errorBanner.classList.add("visible");
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined && text !== null) {
      node.textContent = String(text);
    }
    return node;
  }

  function formatUSD(n) {
    var num = Number(n || 0);
    return "$" + num.toFixed(2);
  }

  function renderApps(apps) {
    appsContainer.innerHTML = "";
    appsCount.textContent = apps.length + (apps.length === 1 ? " app" : " apps");
    if (!apps.length) {
      appsContainer.appendChild(el("div", "empty", "No deployed apps found."));
      return;
    }

    var table = document.createElement("table");
    var thead = document.createElement("thead");
    var headRow = document.createElement("tr");
    ["Org", "App", "Active Release", "Color", "Spend / Cap", "Halted"].forEach(function(h) {
      headRow.appendChild(el("th", "", h));
    });
    thead.appendChild(headRow);
    table.appendChild(thead);

    var tbody = document.createElement("tbody");
    apps.forEach(function(app) {
      var tr = document.createElement("tr");
      var route = app.route || {};
      var budget = app.budget || {};
      var color = route.active_color || "-";
      var colorClass = color === "blue" ? "badge badge-blue" : (color === "green" ? "badge badge-green" : "badge badge-muted");
      var halted = Boolean(route.halted || budget.halted);
      var spendText = formatUSD(budget.current_usd) + " / " + (budget.max_monthly_usd > 0 ? formatUSD(budget.max_monthly_usd) : "unlimited");

      tr.appendChild(el("td", "mono", app.org_id || ""));
      tr.appendChild(el("td", "mono", app.app_name || ""));
      tr.appendChild(el("td", "mono", route.active_release_id || "-"));

      var colorCell = document.createElement("td");
      colorCell.appendChild(el("span", colorClass, color));
      tr.appendChild(colorCell);

      tr.appendChild(el("td", "mono", spendText));

      var haltedCell = document.createElement("td");
      haltedCell.appendChild(el("span", halted ? "badge badge-red" : "badge badge-green", halted ? "halted" : "active"));
      tr.appendChild(haltedCell);

      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    appsContainer.appendChild(table);
  }

  function renderEvents(events) {
    eventsContainer.innerHTML = "";
    eventsCount.textContent = events.length + (events.length === 1 ? " event" : " events");
    if (!events.length) {
      eventsContainer.appendChild(el("div", "empty", "No audit events recorded."));
      return;
    }

    var table = document.createElement("table");
    var thead = document.createElement("thead");
    var headRow = document.createElement("tr");
    ["Time", "Type", "Org / App", "Release", "Detail"].forEach(function(h) {
      headRow.appendChild(el("th", "", h));
    });
    thead.appendChild(headRow);
    table.appendChild(thead);

    var tbody = document.createElement("tbody");
    events.forEach(function(ev) {
      var tr = document.createElement("tr");
      var ts = ev.timestamp ? new Date(ev.timestamp).toLocaleString() : "-";
      var tenant = (ev.org_id || "") + "/" + (ev.app_name || "");
      tr.appendChild(el("td", "mono", ts));
      tr.appendChild(el("td", "mono", ev.type || ""));
      tr.appendChild(el("td", "mono", tenant));
      tr.appendChild(el("td", "mono", ev.release_id || "-"));
      tr.appendChild(el("td", "", ev.detail || "-"));
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    eventsContainer.appendChild(table);
  }

  function fetchJSON(path) {
    return fetch(path, { headers: authHeaders() }).then(function(res) {
      if (!res.ok) {
        if (res.status === 401) {
          throw new Error("Unauthorized (401): enter a valid Bearer token.");
        }
        throw new Error("Request to " + path + " failed with status " + res.status);
      }
      return res.json();
    });
  }

  function refreshAll() {
    refreshBtn.disabled = true;
    showError("");
    Promise.all([
      fetchJSON("/v1/apps"),
      fetchJSON("/v1/events?limit=25")
    ]).then(function(results) {
      renderApps((results[0] && results[0].apps) || []);
      renderEvents((results[1] && results[1].events) || []);
    }).catch(function(err) {
      showError(err && err.message ? err.message : "Failed to load dashboard data.");
    }).finally(function() {
      refreshBtn.disabled = false;
    });
  }

  refreshBtn.addEventListener("click", refreshAll);
  refreshAll();
})();
</script>
</body>
</html>
`

func (d *Daemon) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, uiHTML)
}
