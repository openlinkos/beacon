// Starts asciinema-player on the session page. The page carries no inline script (its
// Content-Security-Policy forbids them), so the recording's address and the texts come from data
// attributes of the player's element.
(function () {
  "use strict";
  var el = document.getElementById("player");
  if (!el) {
    return;
  }
  var note = document.getElementById("player-note");
  var say = function (text) {
    if (note) {
      note.textContent = text;
      note.hidden = !text;
    }
  };
  var src = el.getAttribute("data-src");
  if (!src) {
    return;
  }
  if (typeof AsciinemaPlayer === "undefined") {
    say(el.getAttribute("data-error") || "");
    return;
  }
  var player;
  try {
    player = AsciinemaPlayer.create(src, el, {
      fit: "width",
      preload: true,
      idleTimeLimit: 2,
      controls: true,
      terminalFontFamily: "'Geist Mono', ui-monospace, SFMono-Regular, Menlo, Consolas, 'Liberation Mono', monospace"
    });
  } catch (e) {
    say(el.getAttribute("data-error") || "");
    return;
  }
  if (player && typeof player.addEventListener === "function") {
    player.addEventListener("error", function () {
      say(el.getAttribute("data-error") || "");
    });
    player.addEventListener("playing", function () {
      say("");
    });
  }
})();
