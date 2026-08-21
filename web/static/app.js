/*
 * Progressive enhancement only.
 *
 * Every flow on this site works with JavaScript disabled:
 *
 *   - Key selection is a link per key, resolved server-side from ?key={id}.
 *   - Client guides are <details> panels, all present and all openable.
 *   - Multi-shell command blocks are stacked, each headed by its shell name.
 *   - The user menu is a <details> disclosure wrapping a real POST form.
 *   - Copy buttons are hidden by CSS until this file marks the document.
 *
 * This file adds convenience on top of that and nothing else. There is no
 * inline script anywhere, which is what lets the CSP stay at script-src 'self'
 * with no nonce, and no fetch() — every state change is a form POST.
 */
(function () {
  "use strict";

  var root = document.documentElement;

  // Reveals the copy buttons. Anything gated on this must be a convenience,
  // never the only route to a piece of functionality.
  root.setAttribute("data-js", "");

  // The copy buttons announce their own result by swapping their label, so the
  // live region has to be in place before the first click.
  document.querySelectorAll(".copy-button").forEach(function (button) {
    button.setAttribute("aria-live", "polite");
  });

  /* ---- preferences ------------------------------------------------------ */

  // Not security-relevant: these only remember which model, client tab, and
  // shell you last had open in the setup picker.
  var CLIENT_KEY = "portal.client";
  var SHELL_KEY = "portal.shell";

  // defaultShell picks the shell to show before any preference is stored,
  // guessing from the platform: Windows users get PowerShell, everyone else sh.
  function defaultShell() {
    var hay = (navigator.platform || "") + " " + (navigator.userAgent || "");
    return /win/i.test(hay) ? "powershell" : "sh";
  }

  function readPref(name) {
    try {
      return window.localStorage.getItem(name);
    } catch (e) {
      return null; // storage disabled or partitioned; fall back to defaults
    }
  }

  function writePref(name, value) {
    try {
      window.localStorage.setItem(name, value);
    } catch (e) {
      /* ignore */
    }
  }

  /* ---- copy buttons ----------------------------------------------------- */

  function sourceFor(button) {
    var id = button.getAttribute("data-copy-target");
    return id ? document.getElementById(id) : null;
  }

  function flash(button, message) {
    var original = button.getAttribute("data-original-label");
    if (original === null) {
      original = button.textContent;
      button.setAttribute("data-original-label", original);
    }
    button.textContent = message;
    button.setAttribute("data-copied", "true");
    window.setTimeout(function () {
      button.textContent = original;
      button.removeAttribute("data-copied");
    }, 1400);
  }

  function copy(button) {
    var source = sourceFor(button);
    if (!source) return;

    // innerText on an element that is not being rendered falls back to
    // textContent, so a snippet inside a closed <details> still copies.
    var text = source.innerText;
    if (!text) return;

    // navigator.clipboard requires a secure context, which excludes plain-http
    // development. Fall back to a selection-based copy there.
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(
        function () { flash(button, "Copied"); },
        function () { flash(button, "Press ⌘/Ctrl+C"); }
      );
      return;
    }

    var range = document.createRange();
    range.selectNodeContents(source);
    var selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    flash(button, "Press ⌘/Ctrl+C");
  }

  document.addEventListener("click", function (event) {
    var button = event.target.closest(".copy-button");
    if (button) copy(button);
  });

  /* ---- model + client picker -------------------------------------------- */

  // Upgrades the stacked model×client panels into a pair of segmented controls
  // that select which single panel is shown. Panels are never removed from the
  // DOM, only hidden, so in-page search and printing still find them all.
  var MODEL_KEY = "portal.model";

  // arrowNav wires roving-tabindex arrow-key movement onto a strip of tabs.
  function arrowNav(tabs, onSelect) {
    return function (event) {
      var index = tabs.indexOf(document.activeElement);
      if (index < 0) return;
      var next = null;
      if (event.key === "ArrowRight" || event.key === "ArrowDown") next = (index + 1) % tabs.length;
      if (event.key === "ArrowLeft" || event.key === "ArrowUp") next = (index - 1 + tabs.length) % tabs.length;
      if (event.key === "Home") next = 0;
      if (event.key === "End") next = tabs.length - 1;
      if (next === null) return;
      event.preventDefault();
      tabs[next].focus();
      onSelect(tabs[next]);
    };
  }

  document.querySelectorAll("[data-picker]").forEach(function (picker) {
    var controls = picker.querySelector("[data-picker-controls]");
    var panels = Array.prototype.slice.call(picker.querySelectorAll("[data-guide-panel]"));
    if (panels.length === 0) return;

    var modelTabs = Array.prototype.slice.call(
      picker.querySelectorAll("[data-model-tabs] .guide-tab")
    );
    var clientTabs = Array.prototype.slice.call(
      picker.querySelectorAll("[data-client-tabs] .guide-tab")
    );
    if (clientTabs.length === 0) return;

    var shellTabs = Array.prototype.slice.call(
      picker.querySelectorAll("[data-shell-tabs] .guide-tab")
    );

    if (controls) controls.hidden = false;
    picker.setAttribute("data-enhanced", "");
    [modelTabs, clientTabs, shellTabs].forEach(function (strip) {
      strip.forEach(function (tab) {
        tab.setAttribute("role", "tab");
        if (tab.parentElement) tab.parentElement.setAttribute("role", "presentation");
      });
    });

    // The first panel's data-model is the default model; data-default-client
    // (or the first client tab) is the default client.
    var current = {
      model: panels[0].getAttribute("data-model"),
      client: picker.getAttribute("data-default-client") ||
        clientTabs[0].getAttribute("data-client-target"),
      shell: defaultShell(),
    };

    function markStrip(tabs, attr, value) {
      tabs.forEach(function (tab) {
        var active = tab.getAttribute(attr) === value;
        tab.setAttribute("aria-selected", active ? "true" : "false");
        tab.tabIndex = active ? 0 : -1;
      });
    }

    function apply() {
      panels.forEach(function (panel) {
        var on = panel.getAttribute("data-model") === current.model &&
          panel.getAttribute("data-client") === current.client;
        panel.classList.toggle("is-active", on);
      });
      // The shell preference is a picker-wide attribute; CSS hides the snippet
      // blocks whose data-shell does not match. Shell-agnostic blocks (config
      // files, GUI values) carry no data-shell, so they are never hidden.
      picker.setAttribute("data-shell", current.shell);
      markStrip(modelTabs, "data-model-target", current.model);
      markStrip(clientTabs, "data-client-target", current.client);
      markStrip(shellTabs, "data-shell-target", current.shell);
    }

    function setModel(value, remember) {
      // Guard against a remembered value no client offers any more.
      if (!modelTabs.some(function (t) { return t.getAttribute("data-model-target") === value; }) &&
          !panels.some(function (p) { return p.getAttribute("data-model") === value; })) return;
      current.model = value;
      if (remember) writePref(MODEL_KEY, value);
      apply();
    }

    function setClient(value, remember) {
      if (!clientTabs.some(function (t) { return t.getAttribute("data-client-target") === value; })) return;
      current.client = value;
      if (remember) writePref(CLIENT_KEY, value);
      apply();
    }

    function setShell(value, remember) {
      if (!shellTabs.some(function (t) { return t.getAttribute("data-shell-target") === value; })) return;
      current.shell = value;
      if (remember) writePref(SHELL_KEY, value);
      apply();
    }

    if (modelTabs.length) {
      picker.querySelector("[data-model-tabs]").addEventListener("click", function (event) {
        var tab = event.target.closest(".guide-tab");
        if (tab) setModel(tab.getAttribute("data-model-target"), true);
      });
      picker.querySelector("[data-model-tabs]").addEventListener("keydown",
        arrowNav(modelTabs, function (tab) { setModel(tab.getAttribute("data-model-target"), true); }));
    }
    picker.querySelector("[data-client-tabs]").addEventListener("click", function (event) {
      var tab = event.target.closest(".guide-tab");
      if (tab) setClient(tab.getAttribute("data-client-target"), true);
    });
    picker.querySelector("[data-client-tabs]").addEventListener("keydown",
      arrowNav(clientTabs, function (tab) { setClient(tab.getAttribute("data-client-target"), true); }));

    if (shellTabs.length) {
      picker.querySelector("[data-shell-tabs]").addEventListener("click", function (event) {
        var tab = event.target.closest(".guide-tab");
        if (tab) setShell(tab.getAttribute("data-shell-target"), true);
      });
      picker.querySelector("[data-shell-tabs]").addEventListener("keydown",
        arrowNav(shellTabs, function (tab) { setShell(tab.getAttribute("data-shell-target"), true); }));
    }

    // Restore remembered choices, falling back to the defaults above.
    var rememberedModel = readPref(MODEL_KEY);
    if (rememberedModel) setModel(rememberedModel, false);
    var rememberedClient = readPref(CLIENT_KEY);
    if (rememberedClient) setClient(rememberedClient, false);
    var rememberedShell = readPref(SHELL_KEY);
    if (rememberedShell) setShell(rememberedShell, false);

    apply();
  });

  /* ---- user menu -------------------------------------------------------- */

  // The menu is a <details>, so it already opens and closes without this. All
  // that is added here is dismissing it the way a menu is expected to dismiss.
  document.querySelectorAll("[data-usermenu]").forEach(function (menu) {
    document.addEventListener("click", function (event) {
      if (menu.open && !menu.contains(event.target)) menu.open = false;
    });
    menu.addEventListener("keydown", function (event) {
      if (event.key !== "Escape" || !menu.open) return;
      menu.open = false;
      var trigger = menu.querySelector("summary");
      if (trigger) trigger.focus();
    });
  });
})();
