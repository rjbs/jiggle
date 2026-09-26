<h1>Albums</h1>
<ul class="album-list">
% for my $album (@$albums) {
  <li>
    <a href="/albums/<%= $album->{slug} %>/">
      <img src="<%= $site->rendition_url($album->{cover}, 'h480.webp') %>" loading="lazy" alt="">
      <span class="album-title"><%= $album->{title} %></span>
      <span class="count"><%= scalar $album->{photos}->@* %> photo<%= $album->{photos}->@* == 1 ? '' : 's' %></span>
    </a>
  </li>
% }
</ul>
