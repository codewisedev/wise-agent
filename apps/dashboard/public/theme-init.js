// Applies the saved theme before first paint to avoid a light/dark flash.
// Kept as an external file so the Content-Security-Policy can stay script-src 'self'.
(function () {
  var pref = "dark";
  try {
    var saved = localStorage.getItem("wise-theme");
    if (saved === "dark" || saved === "light" || saved === "system") pref = saved;
  } catch (e) {}
  document.documentElement.dataset.theme = pref;
})();
