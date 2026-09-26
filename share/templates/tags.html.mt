<h1>Tags</h1>
<ul class="tag-cloud">
% for my $tag (@$tags) {
  <li><a href="/tags/<%= $tag->{slug} %>/"><%= $tag->{name} %></a> <span class="count"><%= scalar $tag->{photos}->@* %></span></li>
% }
</ul>
