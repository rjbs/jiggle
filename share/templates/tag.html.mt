<h1>Tagged &ldquo;<%= $tag->{name} %>&rdquo;</h1>
<%= $site->partial('_grid', { photos => $tag->{photos} }) %>
