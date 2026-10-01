% # Each item is a photo, or an album (a hash, as from $site->albums), which
% # is shown by its cover and links to the album.  -- claude, 2026-09-30
<ul class="grid">
% for my $p (@$photos) {
%   if (ref $p eq 'HASH') {
%     my $cover = $p->{cover};
%     my ($w, $h) = $site->rendition_size($cover, 'h480.webp');
%     my $n = scalar $p->{photos}->@*;
%     my $label = sprintf '%s (%d photo%s)', $p->{title}, $n, $n == 1 ? '' : 's';
  <li class="album" style="--ar: <%= sprintf '%.4f', $w / $h %>"><a href="/albums/<%= $p->{slug} %>/" title="<%= $label %>"><img src="<%= $site->rendition_url($cover, 'h480.webp') %>" width="<%= $w %>" height="<%= $h %>" loading="lazy" alt="<%= $label %>"></a></li>
%   } else {
%     my ($w, $h) = $site->rendition_size($p, 'h480.webp');
  <li class="<%= $p->is_video ? 'video' : '' %>" style="--ar: <%= sprintf '%.4f', $w / $h %>"><a href="<%= $site->photo_url($p) %>" title="<%= $site->display_title($p) %>"><img src="<%= $site->rendition_url($p, 'h480.webp') %>" width="<%= $w %>" height="<%= $h %>" loading="lazy" alt="<%= $site->display_title($p) %>"></a></li>
%   }
% }
</ul>
