// dashboard.js polls /partials/dashboard and swaps the response
// into #dashboard-container. Plain fetch + DOM replace keeps the
// dependency surface zero (no HTMX bundle, no build step) while
// honouring the brief's 5–10 second auto-refresh requirement. The
// polling pauses while the tab is hidden so a minimised window does
// not keep hammering the SQLite-backed endpoint, and falls back to
// exponential backoff (capped at 60s) on consecutive failures so a
// crashed daemon does not turn every open tab into a hot retry loop.
//
// taskOps provides CSRF-aware fetch wrappers for mutating task
// operations (dispatch, promote, reopen, add, update-priority, delete).
// All mutating calls include the X-CSRF-Token header read from the
// marunage_csrf cookie that the server sets on every GET response.

(function () {
  "use strict";

  // ---------------------------------------------------------------------------
  // Dashboard polling
  // ---------------------------------------------------------------------------

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

  // ---------------------------------------------------------------------------
  // CSRF-aware task operation helpers
  // ---------------------------------------------------------------------------

  // csrfToken reads the marunage_csrf cookie value.
  // The cookie is HttpOnly=false so JavaScript can echo it in the
  // X-CSRF-Token header (double-submit cookie pattern).
  function csrfToken() {
    var match = document.cookie.match(/(?:^|;\s*)marunage_csrf=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : "";
  }

  // apiPost sends a POST request to path with an optional JSON body
  // and the CSRF token header. Returns a Promise resolving to the
  // parsed JSON response.
  function apiPost(path, body) {
    var init = {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "X-CSRF-Token": csrfToken(),
        "Content-Type": "application/json"
      }
    };
    if (body !== undefined) {
      init.body = JSON.stringify(body);
    }
    return fetch(path, init).then(function (resp) {
      return resp.json().then(function (data) {
        if (!resp.ok) {
          throw new Error(data.error || "HTTP " + resp.status);
        }
        return data;
      });
    });
  }

  // apiPatch sends a PATCH request (for priority updates).
  function apiPatch(path, body) {
    return fetch(path, {
      method: "PATCH",
      credentials: "same-origin",
      headers: {
        "X-CSRF-Token": csrfToken(),
        "Content-Type": "application/json"
      },
      body: JSON.stringify(body)
    }).then(function (resp) {
      return resp.json().then(function (data) {
        if (!resp.ok) {
          throw new Error(data.error || "HTTP " + resp.status);
        }
        return data;
      });
    });
  }

  // apiDelete sends a DELETE request.
  function apiDelete(path) {
    return fetch(path, {
      method: "DELETE",
      credentials: "same-origin",
      headers: {
        "X-CSRF-Token": csrfToken()
      }
    }).then(function (resp) {
      return resp.json().then(function (data) {
        if (!resp.ok) {
          throw new Error(data.error || "HTTP " + resp.status);
        }
        return data;
      });
    });
  }

  // disableElement temporarily disables a button/input while the request
  // is in flight so accidental double-clicks do not send duplicate requests.
  function disableElement(el, disabled) {
    if (el) {
      el.disabled = disabled;
    }
  }

  // refreshNow cancels any pending poll timer and triggers an immediate
  // refresh so the user sees updated state right after a mutation.
  function refreshNow() {
    failures = 0;
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
    refresh();
  }

  // taskOps is the public namespace exposed to onclick handlers in
  // dashboard.html. All methods call refreshNow() on success so the
  // task list reflects the mutation without waiting for the next poll.
  window.taskOps = {

    // dispatch(id, btn) - transitions task from pending to running.
    dispatch: function (id, btn) {
      disableElement(btn, true);
      apiPost("/api/tasks/" + id + "/dispatch")
        .then(function () {
          refreshNow();
        })
        .catch(function (err) {
          disableElement(btn, false);
          alert("Dispatch failed: " + err.message);
        });
    },

    // promote(id, btn) - transitions task from skipped to pending.
    promote: function (id, btn) {
      disableElement(btn, true);
      apiPost("/api/tasks/" + id + "/promote")
        .then(function () {
          refreshNow();
        })
        .catch(function (err) {
          disableElement(btn, false);
          alert("Promote failed: " + err.message);
        });
    },

    // reopen(id, btn) - transitions task from done/failed to pending.
    reopen: function (id, btn) {
      disableElement(btn, true);
      apiPost("/api/tasks/" + id + "/reopen")
        .then(function () {
          refreshNow();
        })
        .catch(function (err) {
          disableElement(btn, false);
          alert("Reopen failed: " + err.message);
        });
    },

    // addTask(event) - submits the add-task form.
    addTask: function (event) {
      event.preventDefault();
      var form = event.target;
      var title = form.querySelector("[name=title]").value.trim();
      var body = form.querySelector("[name=body]").value;
      var cwd = form.querySelector("[name=cwd]").value.trim();
      var priority = parseInt(form.querySelector("[name=priority]").value, 10) || 0;
      var feedback = document.getElementById("add-form-feedback");
      var submitBtn = form.querySelector("[type=submit]");

      if (!title) {
        if (feedback) { feedback.textContent = "Title is required."; }
        return;
      }

      disableElement(submitBtn, true);
      if (feedback) { feedback.textContent = ""; }

      apiPost("/api/tasks", { title: title, body: body, cwd: cwd, priority: priority })
        .then(function () {
          form.reset();
          if (feedback) { feedback.textContent = "Task added."; }
          refreshNow();
        })
        .catch(function (err) {
          if (feedback) { feedback.textContent = "Error: " + err.message; }
        })
        .finally(function () {
          disableElement(submitBtn, false);
        });
    },

    // updatePriority(id, value, input) - patches the priority of a task.
    updatePriority: function (id, value, input) {
      var priority = parseInt(value, 10);
      if (isNaN(priority)) { return; }
      disableElement(input, true);
      apiPatch("/api/tasks/" + id + "/priority", { priority: priority })
        .then(function () {
          refreshNow();
        })
        .catch(function (err) {
          disableElement(input, false);
          alert("Priority update failed: " + err.message);
        });
    },

    // deleteTask(id, btn) - deletes a task after confirmation.
    deleteTask: function (id, btn) {
      if (!confirm("Delete task #" + id + "? This cannot be undone.")) {
        return;
      }
      disableElement(btn, true);
      apiDelete("/api/tasks/" + id)
        .then(function () {
          refreshNow();
        })
        .catch(function (err) {
          disableElement(btn, false);
          alert("Delete failed: " + err.message);
        });
    }
  };

})();
