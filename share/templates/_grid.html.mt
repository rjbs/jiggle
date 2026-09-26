<ul class="grid">
% for my $p (@$photos) {
  <li><a href="<%= $site->photo_url($p) %>"><img src="<%= $site->rendition_url($p, 'sq300.webp') %>" width="300" height="300" loading="lazy" alt="<%= $site->display_title($p) %>"></a></li>
% }
</ul>
