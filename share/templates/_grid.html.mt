<ul class="grid">
% for my $p (@$photos) {
%   my ($w, $h) = $site->rendition_size($p, 'h480.webp');
  <li style="--ar: <%= sprintf '%.4f', $w / $h %>"><a href="<%= $site->photo_url($p) %>"><img src="<%= $site->rendition_url($p, 'h480.webp') %>" width="<%= $w %>" height="<%= $h %>" loading="lazy" alt="<%= $site->display_title($p) %>"></a></li>
% }
</ul>
