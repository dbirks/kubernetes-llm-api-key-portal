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

  // Neither of these is security-relevant: one remembers which client tab you
  // were reading, the other which shell you use.
  var CLIENT_KEY = "portal.client";
  var SHELL_KEY = "portal.shell";

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

  /* ---- client guide tabs ------------------------------------------------ */

  // Upgrades the <details> list into a tab strip. Panels are never removed from
  // the DOM, only closed, so in-page search and printing still find them.
  document.querySelectorAll("[data-guides]").forEach(function (container) {
    var tabList = container.querySelector("[data-guide-tabs]");
    var panels = Array.prototype.slice.call(
      container.querySelectorAll("[data-guide-panel]")
    );
    if (!tabList || panels.length === 0) return;

    var tabs = Array.prototype.slice.call(tabList.querySelectorAll(".guide-tab"));
    if (tabs.length === 0) return;

    tabList.hidden = false;
    tabList.setAttribute("role", "tablist");
    tabs.forEach(function (tab) {
      tab.setAttribute("role", "tab");
      tab.parentElement.setAttribute("role", "presentation");
    });
    container.setAttribute("data-enhanced", "");

    function select(id, remember) {
      panels.forEach(function (panel) {
        panel.open = panel.id === id;
      });
      tabs.forEach(function (tab) {
        var active = tab.getAttribute("data-guide-target") === id;
        tab.setAttribute("aria-selected", active ? "true" : "false");
        // Roving tabindex: one stop for the strip, arrow keys move within it.
        tab.tabIndex = active ? 0 : -1;
      });
      if (remember) writePref(CLIENT_KEY, id);
    }

    tabList.addEventListener("click", function (event) {
      var tab = event.target.closest(".guide-tab");
      if (tab) select(tab.getAttribute("data-guide-target"), true);
    });

    tabList.addEventListener("keydown", function (event) {
      var index = tabs.indexOf(document.activeElement);
      if (index < 0) return;
      var next = null;
      if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
      if (event.key === "ArrowLeft") next = (index - 1 + tabs.length) % tabs.length;
      if (event.key === "Home") next = 0;
      if (event.key === "End") next = tabs.length - 1;
      if (next === null) return;
      event.preventDefault();
      tabs[next].focus();
      select(tabs[next].getAttribute("data-guide-target"), true);
    });

    var remembered = readPref(CLIENT_KEY);
    var known = panels.some(function (panel) { return panel.id === remembered; });
    select(known ? remembered : panels[0].id, false);
  });

  /* ---- shell toggle ----------------------------------------------------- */

  // Rendered only when a guide ships more than one command block. Without this
  // upgrade the blocks are simply stacked, each under its own shell heading.
  function looksLikePowerShell(name) {
    return /power ?shell|pwsh|ps1/i.test(name || "");
  }

  var prefersPowerShell = /Windows/i.test(navigator.userAgent || "");

  document.querySelectorAll("[data-shell-tabs]").forEach(function (tabList) {
    var tabs = Array.prototype.slice.call(tabList.querySelectorAll(".shell-tab"));
    if (tabs.length < 2) return;

    var panels = tabs
      .map(function (tab) {
        return document.getElementById(tab.getAttribute("data-shell-target") + "-panel");
      })
      .filter(Boolean);
    if (panels.length !== tabs.length) return;

    tabList.hidden = false;
    tabList.setAttribute("role", "tablist");
    tabs.forEach(function (tab) {
      tab.setAttribute("role", "tab");
      tab.parentElement.setAttribute("role", "presentation");
    });

    function select(lang, remember) {
      tabs.forEach(function (tab, i) {
        var active = tab.getAttribute("data-shell-lang") === lang;
        tab.setAttribute("aria-selected", active ? "true" : "false");
        tab.tabIndex = active ? 0 : -1;
        panels[i].hidden = !active;
      });
      if (remember) writePref(SHELL_KEY, lang);
    }

    function langs() {
      return tabs.map(function (tab) { return tab.getAttribute("data-shell-lang"); });
    }

    tabList.addEventListener("click", function (event) {
      var tab = event.target.closest(".shell-tab");
      if (tab) select(tab.getAttribute("data-shell-lang"), true);
    });

    var available = langs();
    var remembered = readPref(SHELL_KEY);
    var initial = available.indexOf(remembered) >= 0 ? remembered : null;
    if (initial === null && prefersPowerShell) {
      available.forEach(function (lang) {
        if (initial === null && looksLikePowerShell(lang)) initial = lang;
      });
    }
    select(initial === null ? available[0] : initial, false);
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
