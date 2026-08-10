/*
 * Progressive enhancement only.
 *
 * Every flow on this site works with JavaScript disabled. This file adds two
 * conveniences and nothing else:
 *
 *   1. Clipboard buttons for snippets.
 *   2. A tab strip over the guide list, which is plain <details> without JS.
 *
 * There is no inline script anywhere, which is what lets the CSP stay at
 * script-src 'self' with no nonce.
 */
(function () {
  "use strict";

  /* ---- copy buttons ---- */

  function textFor(button) {
    var id = button.getAttribute("data-copy-target");
    if (!id) return null;
    var source = document.getElementById(id);
    return source ? source.innerText : null;
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
    }, 2000);
  }

  function copy(button) {
    var text = textFor(button);
    if (text === null) return;

    // navigator.clipboard requires a secure context, which excludes plain-http
    // development. Fall back to a selection-based copy there.
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(
        function () { flash(button, "Copied"); },
        function () { flash(button, "Press ⌘/Ctrl+C"); }
      );
      return;
    }
    var source = document.getElementById(button.getAttribute("data-copy-target"));
    if (!source) return;
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

  /* ---- guide tabs ---- */

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

    function select(id) {
      panels.forEach(function (panel) {
        panel.open = panel.id === id;
      });
      tabs.forEach(function (tab) {
        var active = tab.getAttribute("data-guide-target") === id;
        tab.setAttribute("aria-selected", active ? "true" : "false");
        // Roving tabindex: one stop for the strip, arrow keys move within it.
        tab.tabIndex = active ? 0 : -1;
      });
    }

    tabList.addEventListener("click", function (event) {
      var tab = event.target.closest(".guide-tab");
      if (tab) select(tab.getAttribute("data-guide-target"));
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
      select(tabs[next].getAttribute("data-guide-target"));
    });

    select(panels[0].id);
  });
})();
