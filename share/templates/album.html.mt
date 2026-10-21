<h1><%= $album->{title} %></h1>
<p class="album-actions"><button type="button" class="viewer-open" data-list="/albums/<%= $album->{slug} %>/photos.json" data-photo="<%= $site->viewer_entry_json($album, $album->{photos}[0]) %>" data-position="1">Full screen</button></p>
<%= $site->description_html($album->{description}) %>
<%= $site->partial('_grid', { photos => $album->{photos}, in_album => $album }) %>
