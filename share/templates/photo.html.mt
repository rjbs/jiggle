% my ($w, $h) = $site->rendition_size($photo, '2048.webp');
<article class="photo" data-pagefind-body data-pagefind-meta="image:<%= $site->rendition_url($photo, 'h480.webp') %>">
  <figure>
% if ($photo->is_video) {
%   my ($vw, $vh) = $site->rendition_size($photo, 'video.mp4');
    <video controls playsinline preload="metadata"
           poster="<%= $site->rendition_url($photo, '2048.webp') %>"
           width="<%= $vw %>" height="<%= $vh %>"
           style="--ar: <%= sprintf '%.4f', $vw / $vh %>">
      <source src="<%= $site->rendition_url($photo, 'video.mp4') %>" type="video/mp4">
    </video>
% } else {
    <img src="<%= $site->rendition_url($photo, '1024.webp') %>"
         srcset="<%= $site->srcset($photo) %>"
         sizes="(max-width: 1200px) 100vw, 1200px"
         width="<%= $w %>" height="<%= $h %>"
         style="--ar: <%= sprintf '%.4f', $w / $h %>; --w: <%= $w %>px"
         alt="<%= $site->display_title($photo) %>">
% }
  </figure>
  <nav class="neighbors" data-pagefind-ignore>
% if ($newer) {
    <a rel="prev" href="<%= $site->photo_url($newer) %>">&larr; newer</a>
% }
% if ($older) {
    <a rel="next" href="<%= $site->photo_url($older) %>">older &rarr;</a>
% }
  </nav>
  <div class="photo-info">
    <div class="photo-text">
      <h1><%= $site->display_title($photo) %></h1>
      <%= $site->description_html($photo->description) %>
    </div>
    <dl class="photo-meta">
% if (defined $photo->taken) {
%   my $month = $site->month_of($photo);
      <dt data-pagefind-ignore>Taken</dt>
      <dd>
%   if ($month) {
        <a href="<%= $site->month_url(@$month) %>"><%= $site->display_date($photo) %></a>
%   } else {
        <%= $site->display_date($photo) %>
%   }
      </dd>
% }
% if (@$albums) {
      <dt data-pagefind-ignore>Albums</dt>
      <dd>
%   for my $album (@$albums) {
        <a href="/albums/<%= $album->{slug} %>/"><%= $album->{title} %></a>
%   }
      </dd>
% }
% if ($photo->tags->@*) {
      <dt data-pagefind-ignore>Tags</dt>
      <dd class="tags">
%   for my $tag ($photo->tags->@*) {
        <a href="/tags/<%= $site->tag_slug($tag) %>/"><%= $tag %></a>
%   }
      </dd>
% }
    </dl>
% if ($location) {
    <div id="photo-map" class="photo-map"></div>
    <script>jiggleMap.single('photo-map', <%= $location->{lat} %>, <%= $location->{lon} %>);</script>
% }
  </div>
</article>
