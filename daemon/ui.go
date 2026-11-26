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
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
  .skip-link {
    position: absolute;
    top: -48px;
    left: 16px;
    background: #2563eb;
    color: #fff;
    padding: 8px 14px;
    border-radius: 6px;
    font-size: 0.875rem;
    font-weight: 600;
    text-decoration: none;
    z-index: 100;
  }
  .skip-link:focus {
    top: 16px;
  }
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
  input[type="password"], input[type="text"], select {
    background: var(--panel);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 8px 12px;
    border-radius: 6px;
    font-size: 0.875rem;
  }
  input[type="password"], input[type="text"] {
    min-width: 220px;
  }
  button {
    background: #2563eb;
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
  button:focus-visible, input:focus-visible, select:focus-visible, a:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
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
  .banner.success {
    background: rgba(34, 197, 94, 0.14);
    border-color: rgba(34, 197, 94, 0.4);
    color: #bbf7d0;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .btn-sm {
    padding: 4px 10px;
    font-size: 0.75rem;
  }
  .btn-secondary {
    background: #1e293b;
    border: 1px solid var(--border);
    color: var(--text);
  }
  .btn-danger {
    background: rgba(239, 68, 68, 0.18);
    border: 1px solid rgba(239, 68, 68, 0.45);
    color: #fecaca;
  }
  section {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 18px 20px;
    margin-bottom: 24px;
  }
  .section-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 14px;
  }
  .section-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
  }
  .section-head h2 {
    margin: 0;
    font-size: 1.05rem;
  }
  .meta {
    color: var(--muted);
    font-size: 0.8rem;
  }
  .table-wrap {
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
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
  @media (max-width: 640px) {
    .container {
      padding: 16px 12px 36px;
    }
    .controls {
      width: 100%;
    }
    input[type="password"], input[type="text"] {
      min-width: 0;
      flex: 1 1 160px;
    }
    section {
      padding: 14px 12px;
    }
  }
</style>
</head>
<body>
<a class="skip-link" href="#main-content">Skip to main content</a>
<div class="container">
  <header>
    <div class="brand">
      <h1>SmallCloud</h1>
      <p>Operator Dashboard</p>
    </div>
    <div class="controls">
      <label class="sr-only" for="token-input">API Bearer Token</label>
      <input id="token-input" type="password" placeholder="Optional Bearer token" aria-label="API Bearer Token">
      <button id="refresh-btn" type="button" aria-label="Refresh dashboard">Refresh</button>
    </div>
  </header>

  <div id="error-banner" class="banner" role="alert" aria-live="assertive"></div>
  <div id="status-banner" class="banner success" role="status" aria-live="polite"></div>

  <main id="main-content">
    <section aria-labelledby="apps-heading">
      <div class="section-head">
        <h2 id="apps-heading">Apps</h2>
        <span id="apps-count" class="meta">0 apps</span>
      </div>
      <div id="apps-container"></div>
    </section>

    <section aria-labelledby="events-heading">
      <div class="section-head">
        <h2 id="events-heading">Recent Audit Events</h2>
        <div class="section-actions">
          <label class="sr-only" for="event-type-filter">Filter audit events by type</label>
          <select id="event-type-filter" aria-label="Filter audit events by type">
            <option value="">All</option>
            <option value="deploy">deploy</option>
            <option value="rollback">rollback</option>
            <option value="canary">canary</option>
            <option value="canary_promoted">canary_promoted</option>
            <option value="canary_rollback">canary_rollback</option>
            <option value="budget_alert">budget_alert</option>
            <option value="budget_halted">budget_halted</option>
            <option value="rollover">rollover</option>
            <option value="delete">delete</option>
            <option value="gitops_push">gitops_push</option>
          </select>
          <span id="events-count" class="meta">0 events</span>
        </div>
      </div>
      <div id="events-container"></div>
    </section>
  </main>
</div>

<script>
(function() {
  var TOKEN_KEY = "smallcloud_bearer_token";
  var tokenInput = document.getElementById("token-input");
  var refreshBtn = document.getElementById("refresh-btn");
  var errorBanner = document.getElementById("error-banner");
  var statusBanner = document.getElementById("status-banner");
  var appsContainer = document.getElementById("apps-container");
  var eventsContainer = document.getElementById("events-container");
  var appsCount = document.getElementById("apps-count");
  var eventsCount = document.getElementById("events-count");
  var eventTypeFilter = document.getElementById("event-type-filter");

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

  function showStatus(msg) {
    if (!msg) {
      statusBanner.textContent = "";
      statusBanner.classList.remove("visible");
      return;
    }
    statusBanner.textContent = msg;
    statusBanner.classList.add("visible");
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
    headRow.innerHTML = '<th scope="col">Org</th><th scope="col">App</th><th scope="col">Active Release</th><th scope="col">Color</th><th scope="col">Spend / Cap</th><th scope="col">Halted</th><th scope="col">Actions</th>';
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
      var appLabel = (app.org_id || "") + "/" + (app.app_name || "");

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

      var actionsCell = document.createElement("td");
      var actionsWrap = el("div", "actions");

      var rollbackBtn = el("button", "btn-sm btn-danger", "Rollback");
      rollbackBtn.type = "button";
      rollbackBtn.setAttribute("aria-label", "Rollback " + appLabel);
      rollbackBtn.addEventListener("click", function() {
        runAppAction("Rollback", "/v1/rollback", { org_id: app.org_id, app_name: app.app_name }, rollbackBtn);
      });
      actionsWrap.appendChild(rollbackBtn);

      var canaryBtn = el("button", "btn-sm btn-secondary", "Reconcile Canary");
      canaryBtn.type = "button";
      canaryBtn.setAttribute("aria-label", "Reconcile Canary " + appLabel);
      canaryBtn.addEventListener("click", function() {
        runAppAction("Reconcile Canary", "/v1/canary/reconcile", { org_id: app.org_id, app_name: app.app_name }, canaryBtn);
      });
      actionsWrap.appendChild(canaryBtn);

      var pruneBtn = el("button", "btn-sm btn-secondary", "Prune");
      pruneBtn.type = "button";
      pruneBtn.setAttribute("aria-label", "Prune " + appLabel);
      pruneBtn.addEventListener("click", function() {
        runAppAction("Prune", "/v1/prune", { org_id: app.org_id, app_name: app.app_name, keep_last: 5 }, pruneBtn);
      });
      actionsWrap.appendChild(pruneBtn);

      actionsCell.appendChild(actionsWrap);
      tr.appendChild(actionsCell);

      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    var tableWrap = el("div", "table-wrap");
    tableWrap.appendChild(table);
    appsContainer.appendChild(tableWrap);
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
    headRow.innerHTML = '<th scope="col">Time</th><th scope="col">Type</th><th scope="col">Org / App</th><th scope="col">Release</th><th scope="col">Detail</th>';
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
    var tableWrap = el("div", "table-wrap");
    tableWrap.appendChild(table);
    eventsContainer.appendChild(tableWrap);
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

  function postJSON(path, payload) {
    var headers = Object.assign({ "Content-Type": "application/json" }, authHeaders());
    return fetch(path, {
      method: "POST",
      headers: headers,
      body: JSON.stringify(payload)
    }).then(function(res) {
      return res.json().catch(function() { return {}; }).then(function(data) {
        if (!res.ok) {
          if (res.status === 401) {
            throw new Error("Unauthorized (401): enter a valid Bearer token.");
          }
          var errMsg = (data && data.error) ? data.error : ("Request to " + path + " failed with status " + res.status);
          throw new Error(errMsg);
        }
        return data;
      });
    });
  }

  function runAppAction(actionName, path, payload, btn) {
    btn.disabled = true;
    showError("");
    showStatus("Running " + actionName + " for " + payload.org_id + "/" + payload.app_name + "...");
    postJSON(path, payload).then(function(data) {
      var tenant = payload.org_id + "/" + payload.app_name;
      var msg = actionName + " completed for " + tenant + ".";
      if (path === "/v1/prune") {
        msg = "Pruned " + (data && typeof data.pruned === "number" ? data.pruned : 0) + " snapshot(s) for " + tenant + ".";
      } else if (path === "/v1/rollback" && data && data.active_color) {
        msg = "Rolled back " + tenant + " to " + data.active_color + " (" + (data.active_release_id || "-") + ").";
      } else if (path === "/v1/canary/reconcile" && data && (data.action || data.reason)) {
        msg = "Canary reconcile for " + tenant + ": " + (data.action || "completed") + (data.reason ? " (" + data.reason + ")" : "") + ".";
      }
      showStatus(msg);
      refreshAll();
    }).catch(function(err) {
      showStatus("");
      showError(err && err.message ? err.message : actionName + " failed.");
    }).finally(function() {
      btn.disabled = false;
    });
  }

  function eventsPath() {
    var selectedType = (eventTypeFilter && eventTypeFilter.value) ? eventTypeFilter.value.trim() : "";
    if (!selectedType) {
      return "/v1/events?limit=25";
    }
    return "/v1/events?limit=25&type=" + encodeURIComponent(selectedType);
  }

  function refreshEvents() {
    showError("");
    fetchJSON(eventsPath()).then(function(data) {
      renderEvents((data && data.events) || []);
    }).catch(function(err) {
      showError(err && err.message ? err.message : "Failed to load audit events.");
    });
  }

  function refreshAll() {
    refreshBtn.disabled = true;
    showError("");
    Promise.all([
      fetchJSON("/v1/apps"),
      fetchJSON(eventsPath())
    ]).then(function(results) {
      renderApps((results[0] && results[0].apps) || []);
      renderEvents((results[1] && results[1].events) || []);
    }).catch(function(err) {
      showError(err && err.message ? err.message : "Failed to load dashboard data.");
    }).finally(function() {
      refreshBtn.disabled = false;
    });
  }

  eventTypeFilter.addEventListener("change", function() {
    refreshEvents();
  });
  refreshBtn.addEventListener("click", function() {
    showStatus("");
    refreshAll();
  });
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
