% my $page_title = $title eq $site->site_title ? $title : "$title \x{2014} " . $site->site_title;
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title><%= $page_title %></title>
<link rel="stylesheet" href="/static/jiggle.css">
% if ($og) {
<meta property="og:type" content="website">
<meta property="og:site_name" content="<%= $site->site_title %>">
<meta property="og:title" content="<%= $og->{title} %>">
<meta property="og:url" content="<%= $og->{url} %>">
<meta property="og:image" content="<%= $og->{image} %>">
<meta property="og:image:width" content="<%= $og->{width} %>">
<meta property="og:image:height" content="<%= $og->{height} %>">
%   if (length $og->{description}) {
<meta property="og:description" content="<%= $og->{description} %>">
<meta name="description" content="<%= $og->{description} %>">
%   }
<meta name="twitter:card" content="summary_large_image">
% }
% if ($map) {
<link rel="stylesheet" href="/static/maplibre/maplibre-gl.css">
<script src="/static/maplibre/maplibre-gl.js"></script>
<script src="/static/jiggle-map.js"></script>
% }
</head>
<body>
<header class="site-header">
  <a class="site-title" href="/"><%= $site->site_title %></a>
  <nav>
    <a href="/albums/">Albums</a>
    <a href="/tags/">Tags</a>
    <a href="/map/">Map</a>
  </nav>
</header>
<main>
<%= $content %>
</main>
</body>
</html>
