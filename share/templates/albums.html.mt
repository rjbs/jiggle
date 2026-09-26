<h1>Albums</h1>
<ul class="album-list">
% for my $album (@$albums) {
  <li>
    <a href="/albums/<%= $album->{slug} %>/">
      <img src="<%= $site->rendition_url($album->{cover}, 'sq300.webp') %>" width="300" height="300" loading="lazy" alt="">
      <span class="album-title"><%= $album->{title} %></span>
      <span class="count"><%= scalar $album->{photos}->@* %> photo<%= $album->{photos}->@* == 1 ? '' : 's' %></span>
    </a>
  </li>
% }
</ul>
