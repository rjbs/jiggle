<nav class="neighbors">
% if ($newer) {
  <a rel="prev" href="<%= $newer->{url} %>">&larr; <%= $newer->{label} %></a>
% }
% if ($older) {
  <a rel="next" href="<%= $older->{url} %>"><%= $older->{label} %> &rarr;</a>
% }
</nav>
