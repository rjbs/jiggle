% # Each item is a photo, or an album (a hash, as from $site->albums), which
% # is shown by its cover and links to the album.  A caption, shown over the
% # tile on hover, gives its title and details.  -- claude, 2026-09-30
% #
% # With in_album, the grid is that album's, and photos link to their pages
% # within it.
<ul class="grid">
% for my $p (@$photos) {
%   my $album = ref $p eq 'HASH';
%   my $shown = $album ? $p->{cover} : $p;
%   my ($w, $h) = $site->rendition_size($shown, 'h480.webp');
%   my ($name, $details) = $site->grid_caption($p);
%   my $class = $album ? 'album' : $p->is_video ? 'video' : '';
  <li class="<%= $class %>" style="--ar: <%= sprintf '%.4f', $w / $h %>"><a href="<%= $album ? "/albums/$p->{slug}/" : $in_album ? $site->album_photo_url($in_album, $p) : $site->photo_url($p) %>"><img src="<%= $site->rendition_url($shown, 'h480.webp') %>" width="<%= $w %>" height="<%= $h %>" loading="lazy" alt="<%= $name %>"><span class="caption" aria-hidden="true"><span class="name"><%= $name %></span><span class="details"><%= $details %></span></span></a></li>
% }
</ul>
