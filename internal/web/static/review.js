// review.js provides the reviewOps namespace used by review.html.
// Promote buttons use data-task-id + event delegation (no inline onclick)
// so the page is compatible with the script-src 'self' CSP policy.

(function () {
  "use strict";

  function csrfToken() {
    var match = document.cookie.match(/(?:^|;\s*)marunage_csrf=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : "";
  }

  function apiPost(path) {
    return fetch(path, {
      method: "POST",
      credentials: "same-origin",
      headers: {
        "X-CSRF-Token": csrfToken(),
        "Content-Type": "application/json"
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

  var reviewOps = {
    promote: function (id, btn) {
      if (btn) {
        btn.disabled = true;
        btn.textContent = "Promoting…";
        btn.setAttribute("aria-busy", "true");
      }
      apiPost("/api/tasks/" + id + "/promote")
        .then(function () {
          window.location.reload();
        })
        .catch(function (err) {
          if (btn) {
            btn.disabled = false;
            btn.textContent = "Promote";
            btn.removeAttribute("aria-busy");
          }
          // Inline feedback instead of alert() to avoid popup-blocker issues.
          var row = btn ? btn.closest("tr") : null;
          if (row) {
            var feedback = row.querySelector(".promote-feedback");
            if (!feedback) {
              feedback = document.createElement("span");
              feedback.className = "promote-feedback hint";
              feedback.setAttribute("aria-live", "assertive");
              btn.parentNode.appendChild(feedback);
            }
            feedback.textContent = "Failed: " + err.message;
          }
        });
    }
  };

  // Event delegation: attach a single listener instead of per-button onclick
  // (required by script-src 'self' CSP). The script is loaded with defer so
  // the DOM is ready when this runs — no DOMContentLoaded wrapper needed.
  document.addEventListener("click", function (e) {
    var btn = e.target.closest(".btn-promote");
    if (!btn) { return; }
    var id = parseInt(btn.dataset.taskId, 10);
    if (isNaN(id)) { return; }
    reviewOps.promote(id, btn);
  });

  window.reviewOps = reviewOps;
}());
