<template>
  <aside class="rounded-md border border-sky-500/30 bg-sky-500/5 p-2.5 text-xs leading-relaxed text-sky-100/90" aria-label="Metric definitions">
    <p class="font-semibold text-sky-200">Arrivals ≠ virtual users ≠ HTTP requests</p>
    <ul class="mt-1 space-y-0.5">
      <li><b>Customer arrival</b> — one synthetic customer journey (browse, availability, reserve or purchase).</li>
      <li><b>HTTP requests</b> — each journey makes several requests (e.g. a purchase ≈ 4–5), so req/s &gt; arrivals/s.</li>
      <li><b>Virtual users (VUs)</b> — journeys in flight at the same moment; max VUs caps concurrency, and arrivals beyond it are <i>dropped</i> (generator saturation, not app capacity).</li>
    </ul>
  </aside>
</template>
