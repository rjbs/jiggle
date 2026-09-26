<h1><%= $album->{title} %></h1>
<%= $site->description_html($album->{description}) %>
<%= $site->partial('_grid', { photos => $album->{photos} }) %>
