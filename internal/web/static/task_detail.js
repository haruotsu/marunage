// task_detail.js — live terminal stream + workspace send for /tasks/{id}.
// Compatible with script-src 'self' CSP (no inline handlers).

(function () {
  "use strict";

  function csrfToken() {
    var match = document.cookie.match(/(?:^|;\s*)marunage_csrf=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : "";
  }

  var outputEl = document.getElementById("terminal-output");
  var sendForm = document.getElementById("terminal-send-form");
  var sendInput = document.getElementById("terminal-input");
  var statusEl = document.getElementById("terminal-status");

  if (!outputEl) { return; }

  var taskId = outputEl.dataset.taskId;
  if (!taskId) { return; }

  // Connect SSE stream.
  var es = new EventSource("/api/tasks/" + taskId + "/stream");

  es.addEventListener("output", function (e) {
    try {
      outputEl.textContent = JSON.parse(e.data);
    } catch (_) {
      outputEl.textContent = e.data;
    }
  });

  es.addEventListener("ping", function () {
    if (statusEl) { statusEl.textContent = "Connected"; }
  });

  es.onerror = function () {
    if (statusEl) { statusEl.textContent = "Disconnected — retrying…"; }
  };

  // Send form.
  if (!sendForm) { return; }

  sendForm.addEventListener("submit", function (e) {
    e.preventDefault();
    var text = sendInput ? sendInput.value.trim() : "";
    if (!text) { return; }

    var submitBtn = sendForm.querySelector("button[type=submit]");
    if (submitBtn) {
      submitBtn.disabled = true;
      submitBtn.textContent = "Sending…";
    }

    fetch("/api/tasks/" + taskId + "/send", {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken()
      },
      body: JSON.stringify({ text: text })
    })
      .then(function (resp) {
        return resp.json().then(function (data) {
          if (!resp.ok) { throw new Error(data.error || "HTTP " + resp.status); }
          return data;
        });
      })
      .then(function () {
        if (sendInput) { sendInput.value = ""; }
      })
      .catch(function (err) {
        if (statusEl) { statusEl.textContent = "Send failed: " + err.message; }
      })
      .finally(function () {
        if (submitBtn) {
          submitBtn.disabled = false;
          submitBtn.textContent = "Send";
        }
      });
  });
}());
