<h1>Archive</h1>
% for my $year (@$years) {
<section class="archive-year">
  <h2><a href="<%= $site->year_url($year->{year}) %>"><%= $year->{year} %></a>
    <span class="count"><%= $year->{count} %> photo<%= $year->{count} == 1 ? '' : 's' %></span></h2>
  <%= $site->partial('_grid', { photos => [ $site->sample(8, map {; $_->{photos}->@* } reverse $year->{months}->@*) ] }) %>
</section>
% }
% if (@$undated) {
<p><a href="/archive/undated/">Undated</a> <span class="count"><%= scalar @$undated %></span></p>
% }
