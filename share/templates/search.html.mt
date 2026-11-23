% # The search box in the header submits here as ?q=.  That's said here, and
% # not in the script, because the Go site builder's templates take comments
% # out of scripts, and the two builders are meant to make the same page.
% # -- claude, 2026-11-22
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

    var q = new URLSearchParams(window.location.search).get('q');
    if (q) ui.triggerSearch(q);
  });
</script>
