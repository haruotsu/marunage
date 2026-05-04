// review.js provides the reviewOps namespace used by review.html.
// It only needs the promote operation (skipped → pending); the CSRF
// pattern mirrors dashboard.js so the cookie-based double-submit token
// works identically.

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
    // promote transitions task id from skipped → pending and reloads
    // the page so the row disappears from the review list.
    promote: function (id, btn) {
      if (btn) { btn.disabled = true; }
      apiPost("/api/tasks/" + id + "/promote")
        .then(function () {
          window.location.reload();
        })
        .catch(function (err) {
          if (btn) { btn.disabled = false; }
          alert("Promote failed: " + err.message);
        });
    }
  };

  window.reviewOps = reviewOps;
}());
