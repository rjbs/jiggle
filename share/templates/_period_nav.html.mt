<nav class="neighbors">
% if ($earlier) {
  <a rel="prev" href="<%= $earlier->{url} %>">&larr; <%= $earlier->{label} %></a>
% }
% if ($later) {
  <a rel="next" href="<%= $later->{url} %>"><%= $later->{label} %> &rarr;</a>
% }
</nav>
