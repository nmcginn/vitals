// The theme toggle (decision 34). The page works without this: it follows
// the system's colour scheme, and the toggle stays hidden. Runs in <head> so
// a stored theme applies before the first paint on every refresh.
(function () {
  var root = document.documentElement;
  var modes = ["auto", "light", "dark"];
  var mode = "auto";
  try {
    mode = localStorage.getItem("vitals-theme") || "auto";
  } catch (e) {} // storage blocked: the toggle still works until reload
  if (modes.indexOf(mode) < 0) mode = "auto";

  function apply() {
    if (mode === "auto") root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", mode);
    var button = document.getElementById("theme");
    if (button) button.textContent = "Theme: " + mode;
  }
  apply();

  document.addEventListener("DOMContentLoaded", function () {
    var button = document.getElementById("theme");
    button.hidden = false;
    apply();
    button.addEventListener("click", function () {
      mode = modes[(modes.indexOf(mode) + 1) % modes.length];
      try {
        localStorage.setItem("vitals-theme", mode);
      } catch (e) {}
      apply();
    });
  });
})();
