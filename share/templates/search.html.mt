<h1>Search</h1>
<link rel="stylesheet" href="/pagefind/pagefind-ui.css">
<script src="/pagefind/pagefind-ui.js"></script>
<div id="search"></div>
<script>
  window.addEventListener('DOMContentLoaded', function () {
    var ui = new PagefindUI({
      element: '#search',
      showImages: true,
      showSubResults: false,
      autofocus: true
    });

    // The search box in the header submits here as ?q=.
    var q = new URLSearchParams(window.location.search).get('q');
    if (q) ui.triggerSearch(q);
  });
</script>
