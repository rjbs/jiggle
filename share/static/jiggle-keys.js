// Arrow keys follow the page's rel="prev" and rel="next" links: left is the
// earlier photo (or month, or year) and right the later one, matching where
// the links sit.  Keys are left alone while typing, while a video has focus (the
// arrows seek it), and with any modifier held, so browser shortcuts still
// work.  -- claude, 2026-09-27
document.addEventListener('keydown', function (e) {
  if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;

  var rel = e.key === 'ArrowLeft' ? 'prev' : e.key === 'ArrowRight' ? 'next' : null;
  if (!rel) return;

  var t = e.target;
  if (t.closest && t.closest('input, textarea, select, video, [contenteditable]')) return;

  var link = document.querySelector('a[rel="' + rel + '"]');
  if (!link) return;

  e.preventDefault();
  window.location.href = link.href;
});
