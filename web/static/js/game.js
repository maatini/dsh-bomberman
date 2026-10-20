/*
 * Bomber Grid browser glue.
 *
 * Rendering, lobby state, chat and game state all travel through the HTMX
 * WebSocket extension. This file only handles what a server-rendered UI cannot:
 * held keyboard state, touch input, clipboard and small connection affordances.
 */
(function () {
  "use strict";

  var KEY_DIRECTIONS = {
    KeyW: "up",
    ArrowUp: "up",
    KeyS: "down",
    ArrowDown: "down",
    KeyA: "left",
    ArrowLeft: "left",
    KeyD: "right",
    ArrowRight: "right"
  };

  var held = [];
  var touchDirection = null;
  var socketOpen = false;
  var toastTimer = null;
  var lastPhase = null;
  var copyResetTimer = null;

  function byId(id) {
    return document.getElementById(id);
  }

  function inputForm() {
    return byId("input-form");
  }

  function currentPhase() {
    var screen = byId("screen");
    return screen ? screen.dataset.phase || "" : "";
  }

  function gameAcceptsInput() {
    return currentPhase() === "playing";
  }

  function isTextEntry(target) {
    if (!target) {
      return false;
    }
    var tag = target.tagName;
    return (
      tag === "INPUT" ||
      tag === "TEXTAREA" ||
      tag === "SELECT" ||
      target.isContentEditable === true
    );
  }

  function sendInput(action, dir) {
    if (!socketOpen) {
      return;
    }
    var form = inputForm();
    if (!form) {
      return;
    }
    var actionField = byId("input-action");
    var dirField = byId("input-dir");
    if (!actionField || !dirField) {
      return;
    }
    actionField.value = action;
    dirField.value = dir || "none";
    if (window.htmx && typeof window.htmx.trigger === "function") {
      window.htmx.trigger(form, "submit");
    }
  }

  function releaseInputs() {
    held.length = 0;
    touchDirection = null;
    sendInput("move", "none");
  }

  function onKeyDown(event) {
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) {
      return;
    }
    if (!gameAcceptsInput() || isTextEntry(event.target)) {
      return;
    }
    var dir = KEY_DIRECTIONS[event.code];
    if (dir) {
      event.preventDefault();
      if (held.indexOf(dir) === -1) {
        held.push(dir);
        sendInput("move", dir);
      }
      return;
    }
    if (event.code === "Space" || event.code === "KeyE") {
      event.preventDefault();
      if (!event.repeat) {
        sendInput("bomb", "none");
      }
      return;
    }
    if (event.code === "KeyX") {
      event.preventDefault();
      if (!event.repeat) {
        sendInput("detonate", "none");
      }
    }
  }

  function onKeyUp(event) {
    var dir = KEY_DIRECTIONS[event.code];
    if (!dir) {
      return;
    }
    var index = held.indexOf(dir);
    if (index !== -1) {
      held.splice(index, 1);
      if (gameAcceptsInput()) {
        sendInput("move", held.length ? held[held.length - 1] : "none");
      } else {
        held.length = 0;
      }
    }
  }

  function bindKeyboard() {
    window.addEventListener("keydown", onKeyDown, { passive: false });
    window.addEventListener("keyup", onKeyUp);
    window.addEventListener("blur", releaseInputs);
    document.addEventListener("visibilitychange", function () {
      if (document.hidden) {
        releaseInputs();
      }
    });
  }

  function bindTouch() {
    document.addEventListener(
      "pointerdown",
      function (event) {
        if (!gameAcceptsInput()) {
          return;
        }
        var button = event.target.closest("[data-dir], [data-action]");
        if (!button) {
          return;
        }
        event.preventDefault();
        if (button.dataset.action === "bomb" || button.dataset.action === "detonate") {
          sendInput(button.dataset.action, "none");
          return;
        }
        var dir = button.dataset.dir;
        if (dir) {
          touchDirection = dir;
          sendInput("move", dir);
        }
      },
      { passive: false }
    );

    function endTouch() {
      if (touchDirection !== null) {
        touchDirection = null;
        sendInput("move", "none");
      }
    }
    window.addEventListener("pointerup", endTouch);
    window.addEventListener("pointercancel", endTouch);
    window.addEventListener("blur", endTouch);
  }

  function bindChat() {
    var form = byId("chat-form");
    if (!form) {
      return;
    }
    document.body.addEventListener("htmx:wsAfterSend", function (event) {
      if (event.target === form) {
        var input = byId("chat-message");
        if (input) {
          input.value = "";
          input.focus();
        }
      }
    });
  }

  function scrollChat(force) {
    var log = byId("chat-log");
    if (!log) {
      return;
    }
    var distance = log.scrollHeight - log.scrollTop - log.clientHeight;
    if (force || distance < 120) {
      log.scrollTop = log.scrollHeight;
    }
  }

  function observeChat() {
    var log = byId("chat-log");
    if (!log) {
      return;
    }
    var observer = new MutationObserver(function () {
      scrollChat(false);
    });
    observer.observe(log, { childList: true, subtree: true, characterData: true });
  }

  function setConnection(state, label) {
    var badge = byId("connection-badge");
    if (!badge) {
      return;
    }
    badge.classList.remove("is-live", "is-connecting", "is-offline");
    badge.classList.add("is-" + state);
    badge.dataset.state = state;
    var text = badge.querySelector(".conn-text");
    if (text) {
      text.textContent = label;
    }
  }

  function bindSocketStatus() {
    document.body.addEventListener("htmx:wsConnecting", function () {
      setConnection("connecting", "Verbinde…");
    });
    document.body.addEventListener("htmx:wsOpen", function () {
      socketOpen = true;
      setConnection("live", "Live");
      scrollChat(true);
    });
    document.body.addEventListener("htmx:wsClose", function () {
      socketOpen = false;
      setConnection("offline", "Offline · Reconnect…");
      held.length = 0;
      touchDirection = null;
    });
    document.body.addEventListener("htmx:wsError", function () {
      socketOpen = false;
      setConnection("offline", "Verbindungsfehler");
    });
  }

  function syncPhase() {
    var phase = currentPhase();
    if (phase !== lastPhase) {
      if (lastPhase === "playing" && phase !== "playing") {
        held.length = 0;
        touchDirection = null;
      }
      lastPhase = phase;
    }
    if (!gameAcceptsInput() && held.length) {
      held.length = 0;
      touchDirection = null;
    }
  }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      var area = document.createElement("textarea");
      area.value = text;
      area.setAttribute("readonly", "");
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.appendChild(area);
      area.select();
      try {
        var ok = document.execCommand("copy");
        document.body.removeChild(area);
        ok ? resolve() : reject(new Error("copy failed"));
      } catch (error) {
        document.body.removeChild(area);
        reject(error);
      }
    });
  }

  function bindCopyLink() {
    var button = byId("copy-link");
    if (!button) {
      return;
    }
    button.addEventListener("click", function () {
      var text = button.dataset.copy || window.location.href;
      copyText(text)
        .then(function () {
          button.textContent = "Kopiert!";
          button.classList.add("is-copied");
          window.clearTimeout(copyResetTimer);
          copyResetTimer = window.setTimeout(function () {
            button.textContent = "Link kopieren";
            button.classList.remove("is-copied");
          }, 1800);
        })
        .catch(function () {
          window.prompt("Einladungslink kopieren:", text);
        });
    });
  }

  function initToast() {
    var toast = byId("toast");
    if (!toast) {
      return;
    }
    var observer = new MutationObserver(function () {
      if (!toast.textContent.trim()) {
        return;
      }
      window.clearTimeout(toastTimer);
      toastTimer = window.setTimeout(function () {
        toast.replaceChildren();
      }, 5200);
    });
    observer.observe(toast, { childList: true, subtree: true, characterData: true });
  }

  function init() {
    bindKeyboard();
    bindTouch();
    bindChat();
    bindSocketStatus();
    bindCopyLink();
    initToast();
    observeChat();
    scrollChat(true);
    syncPhase();
    window.setInterval(syncPhase, 250);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
