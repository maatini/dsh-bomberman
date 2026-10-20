/*
 * Bomber Grid — classic arcade audio, synthesized with the Web Audio API.
 *
 * No audio files are needed. The engine is unlocked by the first user gesture
 * (required by browsers), persists the mute preference locally and derives
 * sound events by diffing the HTML frames that arrive over the HTMX websocket.
 */
(function () {
  "use strict";

  var STORAGE_KEY = "bomber.sound.v1";
  var soundOn = loadPreference();

  var audioContext = null;
  var masterGain = null;
  var noiseBuffer = null;

  var toggleButton = null;
  var skipNextScan = false;
  var state = {
    phase: null,
    tick: null,
    bombs: null,
    lives: null,
    power: null,
    boom: false,
    countdown: null,
    finished: false,
    sudden: false
  };

  function loadPreference() {
    try {
      return window.localStorage.getItem(STORAGE_KEY) !== "off";
    } catch (error) {
      return true;
    }
  }

  function savePreference() {
    try {
      window.localStorage.setItem(STORAGE_KEY, soundOn ? "on" : "off");
    } catch (error) {
      /* private mode: keep the setting for this page only */
    }
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

  function ensureContext() {
    if (!soundOn) {
      return null;
    }
    if (audioContext) {
      if (audioContext.state === "suspended") {
        audioContext.resume().catch(function () {});
      }
      return audioContext;
    }

    var AudioCtor = window.AudioContext || window.webkitAudioContext;
    if (!AudioCtor) {
      return null;
    }
    try {
      audioContext = new AudioCtor();
      masterGain = audioContext.createGain();
      masterGain.gain.value = 0.28;

      var compressor = audioContext.createDynamicsCompressor();
      compressor.threshold.value = -18;
      compressor.knee.value = 18;
      compressor.ratio.value = 6;
      compressor.attack.value = 0.003;
      compressor.release.value = 0.25;

      masterGain.connect(compressor);
      compressor.connect(audioContext.destination);

      noiseBuffer = audioContext.createBuffer(
        1,
        Math.floor(audioContext.sampleRate * 0.75),
        audioContext.sampleRate
      );
      var samples = noiseBuffer.getChannelData(0);
      for (var i = 0; i < samples.length; i++) {
        samples[i] = Math.random() * 2 - 1;
      }
    } catch (error) {
      audioContext = null;
      return null;
    }

    if (audioContext.state === "suspended") {
      audioContext.resume().catch(function () {});
    }
    return audioContext;
  }

  function withAudio(fn) {
    if (!soundOn || document.hidden) {
      return;
    }
    var ctx = ensureContext();
    if (!ctx) {
      return;
    }
    try {
      fn(ctx, masterGain, noiseBuffer);
    } catch (error) {
      /* audio must never break gameplay */
    }
  }

  function tone(ctx, out, frequency, endFrequency, duration, type, volume, delay) {
    var start = ctx.currentTime + (delay || 0);
    var oscillator = ctx.createOscillator();
    var gain = ctx.createGain();
    oscillator.type = type || "square";
    oscillator.frequency.setValueAtTime(Math.max(20, frequency), start);
    if (endFrequency && endFrequency !== frequency) {
      oscillator.frequency.exponentialRampToValueAtTime(
        Math.max(20, endFrequency),
        start + duration
      );
    }
    var peak = Math.max(0.0002, volume || 0.15);
    gain.gain.setValueAtTime(0.0001, start);
    gain.gain.exponentialRampToValueAtTime(peak, start + 0.008);
    gain.gain.exponentialRampToValueAtTime(0.0001, start + duration);
    oscillator.connect(gain);
    gain.connect(out);
    oscillator.start(start);
    oscillator.stop(start + duration + 0.03);
  }

  function noise(ctx, out, buffer, duration, volume, startFrequency, endFrequency, delay) {
    var start = ctx.currentTime + (delay || 0);
    var source = ctx.createBufferSource();
    source.buffer = buffer;
    var filter = ctx.createBiquadFilter();
    filter.type = "lowpass";
    filter.frequency.setValueAtTime(Math.max(40, startFrequency), start);
    filter.frequency.exponentialRampToValueAtTime(
      Math.max(40, endFrequency),
      start + duration
    );
    var gain = ctx.createGain();
    gain.gain.setValueAtTime(Math.max(0.0002, volume || 0.2), start);
    gain.gain.exponentialRampToValueAtTime(0.0001, start + duration);
    source.connect(filter);
    filter.connect(gain);
    gain.connect(out);
    source.start(start);
    source.stop(start + duration + 0.03);
  }

  /* -----------------------------------------------------------------------
   * Arcade sound set
   * --------------------------------------------------------------------- */

  function playCountdown(digit) {
    withAudio(function (ctx, out) {
      var base = digit <= 1 ? 1046 : digit === 2 ? 880 : 698;
      tone(ctx, out, base, base, 0.1, "square", 0.13);
      tone(ctx, out, base / 2, base / 2, 0.11, "triangle", 0.08);
    });
  }

  function playGo() {
    withAudio(function (ctx, out) {
      tone(ctx, out, 523, 1046, 0.24, "square", 0.15);
      tone(ctx, out, 784, 1568, 0.3, "triangle", 0.09, 0.02);
    });
  }

  function playStart() {
    withAudio(function (ctx, out) {
      [392, 523, 659, 784, 1046].forEach(function (frequency, index) {
        tone(ctx, out, frequency, frequency, 0.12, "square", 0.11, index * 0.07);
      });
    });
  }

  function playBomb() {
    withAudio(function (ctx, out) {
      tone(ctx, out, 190, 70, 0.14, "square", 0.16);
      tone(ctx, out, 95, 45, 0.18, "triangle", 0.13, 0.01);
    });
  }

  function playExplosion() {
    withAudio(function (ctx, out, buffer) {
      noise(ctx, out, buffer, 0.5, 0.42, 2600, 120, 0);
      tone(ctx, out, 90, 34, 0.45, "sine", 0.26);
      tone(ctx, out, 240, 50, 0.13, "sawtooth", 0.13);
      noise(ctx, out, buffer, 0.1, 0.16, 4200, 900, 0.01);
    });
  }

  function playPowerup() {
    withAudio(function (ctx, out) {
      [784, 1046, 1318].forEach(function (frequency, index) {
        tone(ctx, out, frequency, frequency, 0.1, "square", 0.1, index * 0.065);
        tone(ctx, out, frequency * 2, frequency * 2, 0.08, "triangle", 0.045, index * 0.065);
      });
    });
  }

  function playHit() {
    withAudio(function (ctx, out, buffer) {
      tone(ctx, out, 280, 55, 0.3, "sawtooth", 0.19);
      noise(ctx, out, buffer, 0.16, 0.16, 1800, 180, 0);
    });
  }

  function playWin() {
    withAudio(function (ctx, out) {
      [523, 659, 784, 1046].forEach(function (frequency, index) {
        tone(ctx, out, frequency, frequency, index === 3 ? 0.42 : 0.16, "square", 0.12, index * 0.12);
        tone(ctx, out, frequency / 2, frequency / 2, index === 3 ? 0.42 : 0.16, "triangle", 0.06, index * 0.12);
      });
      tone(ctx, out, 1318, 1318, 0.42, "triangle", 0.08, 0.38);
    });
  }

  function playLose() {
    withAudio(function (ctx, out) {
      [392, 330, 262, 196].forEach(function (frequency, index) {
        tone(ctx, out, frequency, frequency * 0.94, 0.22, "sawtooth", 0.13, index * 0.16);
      });
    });
  }

  function playDraw() {
    withAudio(function (ctx, out) {
      tone(ctx, out, 392, 392, 0.3, "triangle", 0.14);
      tone(ctx, out, 330, 330, 0.4, "triangle", 0.12, 0.18);
    });
  }

  // Alarm for the sudden-death phase: a rising two-tone siren.
  function playSuddenDeath() {
    withAudio(function (ctx, out, buffer) {
      noise(ctx, out, buffer, 0.5, 0.09, 400, 2400, 0);
      tone(ctx, out, 196, 392, 0.5, "sawtooth", 0.16);
      tone(ctx, out, 392, 784, 0.5, "square", 0.08, 0.28);
    });
  }

  function playToggle() {
    withAudio(function (ctx, out) {
      tone(ctx, out, 1200, 1600, 0.07, "square", 0.08);
    });
  }

  /* -----------------------------------------------------------------------
   * Mute toggle
   * --------------------------------------------------------------------- */

  function renderToggle() {
    if (!toggleButton) {
      return;
    }
    toggleButton.classList.toggle("is-muted", !soundOn);
    toggleButton.setAttribute("aria-pressed", soundOn ? "true" : "false");
    toggleButton.title = soundOn ? "Ton ausschalten (M)" : "Ton einschalten (M)";
    var label = toggleButton.querySelector(".sound-label");
    if (label) {
      label.textContent = soundOn ? "Ton an" : "Ton aus";
    }
  }

  function setSound(next) {
    if (next === soundOn) {
      return;
    }
    if (soundOn) {
      playToggle();
    }
    soundOn = next;
    savePreference();
    if (audioContext && masterGain) {
      masterGain.gain.cancelScheduledValues(audioContext.currentTime);
      masterGain.gain.setTargetAtTime(
        soundOn ? 0.28 : 0.0001,
        audioContext.currentTime,
        0.015
      );
    }
    if (soundOn) {
      ensureContext();
      playToggle();
    }
    renderToggle();
  }

  function toggleSound() {
    setSound(!soundOn);
  }

  function bindToggle() {
    toggleButton = document.getElementById("sound-toggle");
    if (!toggleButton) {
      return;
    }
    renderToggle();
    toggleButton.addEventListener("click", function (event) {
      event.preventDefault();
      toggleSound();
    });
    document.addEventListener("keydown", function (event) {
      if (
        event.code !== "KeyM" ||
        event.repeat ||
        event.metaKey ||
        event.ctrlKey ||
        event.altKey ||
        isTextEntry(event.target)
      ) {
        return;
      }
      event.preventDefault();
      toggleSound();
    });
  }

  function unlockAudio() {
    if (soundOn && toggleButton) {
      ensureContext();
    }
  }

  /* -----------------------------------------------------------------------
   * HTML state diffing → sounds
   * --------------------------------------------------------------------- */

  function readBombs(game) {
    var ids = new Set();
    game.querySelectorAll(".bomb").forEach(function (bomb) {
      ids.add(bomb.id);
    });
    return ids;
  }

  function readLives(game) {
    var lives = {};
    game.querySelectorAll(".player[data-player-id]").forEach(function (player) {
      lives[player.dataset.playerId] = Number(player.dataset.lives || 0);
    });
    return lives;
  }

  function readPowerScore(game) {
    var score = 0;
    game.querySelectorAll(".player[data-player-id]").forEach(function (player) {
      score += Number(player.dataset.maxBombs || 0);
      score += Number(player.dataset.range || 0);
      score += Number(player.dataset.speed || 0);
      score += player.dataset.kick === "true" ? 1 : 0;
      score += player.dataset.remote === "true" ? 1 : 0;
      score += player.dataset.shield === "true" ? 1 : 0;
    });
    return score;
  }

  function readCountdown(game) {
    var el = game.querySelector(".countdown-number");
    return el ? el.textContent.trim() : "";
  }

  function primeState(phase, game) {
    state.phase = phase;
    if (!game) {
      state.tick = null;
      state.bombs = null;
      state.lives = null;
      state.power = null;
      state.boom = false;
      state.countdown = null;
      state.finished = false;
      state.sudden = false;
      return;
    }
    state.tick = Number(game.dataset.tick || 0);
    state.bombs = readBombs(game);
    state.lives = readLives(game);
    state.power = readPowerScore(game);
    state.boom = Boolean(game.querySelector(".cell.boom"));
    state.countdown = readCountdown(game);
    state.finished = game.dataset.finished === "true";
    state.sudden = game.dataset.sudden === "true";
  }

  function playResult(game) {
    var winner = game.dataset.winnerId || "";
    var you = game.dataset.youId || "";
    var spectator = game.dataset.spectator === "true";
    if (game.dataset.draw === "true") {
      playDraw();
    } else if (spectator || winner === you) {
      playWin();
    } else {
      playLose();
    }
  }

  function scan() {
    var screen = document.getElementById("screen");
    if (!screen) {
      return;
    }
    var phase = screen.dataset.phase || "";
    var game = screen.querySelector(".game-screen");

    if (skipNextScan) {
      primeState(phase, game);
      skipNextScan = false;
      return;
    }

    if (!game) {
      state.phase = phase;
      state.tick = null;
      state.bombs = null;
      state.lives = null;
      state.power = null;
      state.boom = false;
      state.countdown = null;
      state.finished = false;
      state.sudden = false;
      return;
    }

    var tick = Number(game.dataset.tick || 0);
    var firstGameFrame = state.tick === null;
    var afterLobby = state.phase === "lobby";
    var rematch = state.tick !== null && tick < state.tick;

    if (firstGameFrame || afterLobby || rematch) {
      primeState(phase, game);
      if (afterLobby || rematch) {
        playStart();
      }
      return;
    }

    if (tick === state.tick && phase === state.phase) {
      return;
    }

    var finished = game.dataset.finished === "true";
    var sudden = game.dataset.sudden === "true";
    if (sudden && !state.sudden) {
      playSuddenDeath();
    }
    state.sudden = sudden;
    var bombs = readBombs(game);
    var newBomb = false;
    if (state.bombs) {
      bombs.forEach(function (id) {
        if (!state.bombs.has(id)) {
          newBomb = true;
        }
      });
    }
    if (newBomb) {
      playBomb();
    }
    state.bombs = bombs;

    var lives = readLives(game);
    if (state.lives) {
      Object.keys(lives).some(function (id) {
        if (state.lives[id] !== undefined && lives[id] < state.lives[id]) {
          playHit();
          return true;
        }
        return false;
      });
    }
    state.lives = lives;

    var power = readPowerScore(game);
    if (state.power !== null && power > state.power) {
      playPowerup();
    }
    state.power = power;

    var boom = Boolean(game.querySelector(".cell.boom"));
    if (boom && !state.boom) {
      playExplosion();
    }
    state.boom = boom;

    var countdown = readCountdown(game);
    if (!finished && state.countdown && countdown && countdown !== state.countdown) {
      playCountdown(Number(countdown));
    }
    if (!finished && state.countdown && !countdown) {
      playGo();
    }
    state.countdown = countdown;

    if (!state.finished && finished) {
      playResult(game);
    }
    state.finished = finished;
    state.phase = phase;
    state.tick = tick;
  }

  function init() {
    bindToggle();
    document.addEventListener("pointerdown", unlockAudio, {
      capture: true,
      passive: true
    });
    document.addEventListener("keydown", unlockAudio, { capture: true });

    if (document.body) {
      document.body.addEventListener("htmx:wsOpen", function () {
        skipNextScan = true;
      });
      document.body.addEventListener("htmx:wsClose", function () {
        skipNextScan = true;
      });
      document.body.addEventListener("htmx:wsAfterMessage", scan);
    }
    var screen = document.getElementById("screen");
    primeState(screen ? screen.dataset.phase || "" : "", document.querySelector(".game-screen"));
    scan();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
