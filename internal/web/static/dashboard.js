// dashboard.js polls /partials/dashboard every refreshIntervalMs and
// swaps the response into #dashboard-container. Plain fetch + DOM
// replace keeps the dependency surface zero (no HTMX bundle, no
// build step) while honouring the brief's 5–10 second auto-refresh
// requirement. The polling pauses while the tab is hidden so a
// minimised window does not keep hammering the SQLite-backed
// endpoint.

(function () {
  "use strict";

  var refreshIntervalMs = 7000;
  var endpoint = "/partials/dashboard";
  var containerId = "dashboard-container";
  var inflight = false;
  var timer = null;

  function refresh() {
    if (inflight) {
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
      })
      .catch(function (err) {
        // Surface the failure quietly: the page is still readable,
        // but we tag the container so a later debug pass can spot
        // refresh failures.
        container.dataset.refreshError = String(err);
      })
      .finally(function () {
        inflight = false;
      });
  }

  function start() {
    if (timer === null) {
      timer = setInterval(refresh, refreshIntervalMs);
    }
  }

  function stop() {
    if (timer !== null) {
      clearInterval(timer);
      timer = null;
    }
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) {
      stop();
    } else {
      refresh();
      start();
    }
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
