// dashboard scripts — keeps the logged-in index's live-grants block fresh.
//
// The block carries data-stream (an SSE endpoint) and data-fragment (the
// grants-list HTML partial). We open an EventSource on the stream; on each
// "chains" event we re-fetch the fragment and swap it into the block, so token
// issuance / refresh / revocation reflect without a page reload.
(() => {
  const block = document.getElementById("grants-block");
  if (!block || !("EventSource" in window)) return;

  const stream = block.dataset.stream;
  const fragURL = block.dataset.fragment;
  if (!stream || !fragURL) return;

  const es = new EventSource(stream);
  es.addEventListener("chains", async () => {
    try {
      const res = await fetch(fragURL, { credentials: "same-origin" });
      if (res.ok) {
        block.innerHTML = await res.text();
      }
    } catch (_) {
      // Leave the stale block in place; the next event will try again.
    }
  });
})();

// "Connect an MCP client" block: the dropdown selects which service the install
// snippets target. We render one .mcp-instructions card-set per service into the
// DOM (all but the first hidden) and toggle visibility here — no round-trip.
(() => {
  const select = document.getElementById("mcp-select");
  if (!select) return;
  const sets = document.querySelectorAll(".mcp-instructions");
  select.addEventListener("change", () => {
    for (const set of sets) set.hidden = set.dataset.mcp !== select.value;
  });
})();

// Copy buttons on the install snippets: copy the preceding <pre> snippet to the
// clipboard and flash "Copied". The install block is static (not re-rendered by
// the grants SSE), so binding once at load is sufficient.
(() => {
  for (const btn of document.querySelectorAll(".copy-btn")) {
    btn.addEventListener("click", async () => {
      const snippet = btn.previousElementSibling;
      if (!snippet) return;
      try {
        await navigator.clipboard.writeText(snippet.innerText);
        const label = btn.textContent;
        btn.textContent = "Copied";
        setTimeout(() => { btn.textContent = label; }, 1200);
      } catch (_) {
        // Clipboard denied/unavailable — leave the button label unchanged.
      }
    });
  }
})();
