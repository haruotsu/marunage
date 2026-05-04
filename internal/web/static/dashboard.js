// dashboard.js polls /partials/dashboard and swaps the response
// into #dashboard-container. Plain fetch + DOM replace keeps the
// dependency surface zero (no HTMX bundle, no build step) while
// honouring the brief's 5–10 second auto-refresh requirement. The
// polling pauses while the tab is hidden so a minimised window does
// not keep hammering the SQLite-backed endpoint, and falls back to
// exponential backoff (capped at 60s) on consecutive failures so a
// crashed daemon does not turn every open tab into a hot retry loop.

(function () {
  "use strict";

  var refreshIntervalMs = 7000;
  var maxBackoffMs = 60000;
  var endpoint = "/partials/dashboard";
  var containerId = "dashboard-container";
  var inflight = false;
  var failures = 0;
  var timer = null;
  var stopped = false;

  function nextDelay() {
    if (failures === 0) {
      return refreshIntervalMs;
    }
    var delay = refreshIntervalMs * Math.pow(2, failures - 1);
    return Math.min(delay, maxBackoffMs);
  }

  function refresh() {
    if (inflight) {
      schedule();
      return;
    }
    var container = document.getElementById(containerId);
    if (!container) {
      return;
    }
    inflight = true;
    fetch(endpoint, { credentials: "same-origin", cache: "no-store" })
      .then(function (resp) {
        if (!resp.ok) {
          throw new Error("dashboard partial: HTTP " + resp.status);
        }
        return resp.text();
      })
      .then(function (html) {
        container.innerHTML = html;
        failures = 0;
        delete container.dataset.refreshError;
      })
      .catch(function (err) {
        failures += 1;
        // Truncate the surfaced detail so a noisy server-side
        // error string cannot grow the dataset attribute past
        // a sane bound.
        container.dataset.refreshError = String(err).slice(0, 200);
      })
      .finally(function () {
        inflight = false;
        schedule();
      });
  }

  function schedule() {
    if (stopped) {
      return;
    }
    if (timer !== null) {
      clearTimeout(timer);
    }
    timer = setTimeout(refresh, nextDelay());
  }

  function start() {
    stopped = false;
    schedule();
  }

  function stop() {
    stopped = true;
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) {
      stop();
    } else {
      // Reset the backoff on tab focus so the user sees a fresh
      // snapshot immediately rather than waiting out the previous
      // failure window.
      failures = 0;
      refresh();
    }
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
