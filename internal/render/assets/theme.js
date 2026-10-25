// Conveniences on top of a page that works without them (decision 34): a
// theme toggle, and cards that stay collapsed across refreshes. Without this
// script the page follows the system's colour scheme, the toggle stays
// hidden, and cards still collapse but reopen on the next refresh. It runs in
// <head> so stored choices apply before the first paint on every refresh.
(function () {
  var root = document.documentElement;

  function load(key) {
    try {
      return localStorage.getItem(key) || "";
    } catch (e) {
      return ""; // storage blocked: choices last until the next reload
    }
  }
  function save(key, value) {
    try {
      localStorage.setItem(key, value);
    } catch (e) {}
  }

  var modes = ["auto", "light", "dark"];
  var mode = load("vitals-theme");
  if (modes.indexOf(mode) < 0) mode = "auto";
  function applyTheme() {
    if (mode === "auto") root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", mode);
    var button = document.getElementById("theme");
    if (button) button.textContent = "Theme: " + mode;
  }
  applyTheme();

  // The closed cards' names, space-separated. Until the cards exist, the
  // stylesheet hides their contents by this attribute.
  var closed = load("vitals-closed");
  if (closed) root.setAttribute("data-closed", closed);

  document.addEventListener("DOMContentLoaded", function () {
    var button = document.getElementById("theme");
    button.hidden = false;
    applyTheme();
    button.addEventListener("click", function () {
      mode = modes[(modes.indexOf(mode) + 1) % modes.length];
      save("vitals-theme", mode);
      applyTheme();
    });

    var cards = document.querySelectorAll("details[data-card]");
    var i;
    for (i = 0; i < cards.length; i++) {
      if ((" " + closed + " ").indexOf(" " + cards[i].dataset.card + " ") >= 0) {
        cards[i].open = false;
      }
      cards[i].addEventListener("toggle", function () {
        var names = [];
        for (var j = 0; j < cards.length; j++) {
          if (!cards[j].open) names.push(cards[j].dataset.card);
        }
        save("vitals-closed", names.join(" "));
      });
    }
    root.removeAttribute("data-closed");
  });
})();
